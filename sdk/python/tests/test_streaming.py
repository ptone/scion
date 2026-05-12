# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

"""Tests for SSE streaming support.

Tests cover:
- SSE line parsing with canned event sequences
- Heartbeat handling (:heartbeat and bare : lines)
- Multi-line data events
- Event type and ID parsing
- Error handling and stream teardown
- Reconnection (last_event_id tracking)
- Sync and async iterators
- Integration with AgentsResource streaming methods
"""

from __future__ import annotations

import json
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from scion._streaming import (
    AsyncSSEIterator,
    SyncSSEIterator,
    _parse_event_data,
    _SSEEvent,
    parse_sse_line,
)
from scion.types.streaming import AgentEvent, LogEntry, StreamEvent

# ---------------------------------------------------------------------------
# SSE line parser unit tests
# ---------------------------------------------------------------------------


class TestParseSSELine:
    """Tests for the low-level SSE line parser."""

    def test_empty_line_signals_boundary(self) -> None:
        """An empty line should signal an event boundary."""
        event = _SSEEvent()
        assert parse_sse_line("", event) is True

    def test_comment_line_skipped(self) -> None:
        """Lines starting with ':' are comments and should be skipped."""
        event = _SSEEvent()
        assert parse_sse_line(":heartbeat", event) is False
        assert event.is_empty()

    def test_bare_colon_comment(self) -> None:
        """A bare ':' is also a comment."""
        event = _SSEEvent()
        assert parse_sse_line(":", event) is False
        assert event.is_empty()

    def test_comment_with_text(self) -> None:
        """Comments can contain arbitrary text after the colon."""
        event = _SSEEvent()
        assert parse_sse_line(": this is a comment", event) is False
        assert event.is_empty()

    def test_data_field(self) -> None:
        """data: lines should append to event data."""
        event = _SSEEvent()
        parse_sse_line('data: {"key": "value"}', event)
        assert event.data == ['{"key": "value"}']

    def test_data_field_strips_single_leading_space(self) -> None:
        """Per SSE spec, a single leading space after ':' is stripped."""
        event = _SSEEvent()
        parse_sse_line("data: hello", event)
        assert event.data == ["hello"]

    def test_data_field_preserves_extra_spaces(self) -> None:
        """Only one leading space is stripped; additional spaces are preserved."""
        event = _SSEEvent()
        parse_sse_line("data:  two spaces", event)
        assert event.data == [" two spaces"]

    def test_data_field_no_space(self) -> None:
        """data: with no space after colon should work."""
        event = _SSEEvent()
        parse_sse_line("data:no-space", event)
        assert event.data == ["no-space"]

    def test_multi_line_data(self) -> None:
        """Multiple data lines should accumulate."""
        event = _SSEEvent()
        parse_sse_line("data: line1", event)
        parse_sse_line("data: line2", event)
        parse_sse_line("data: line3", event)
        assert event.data == ["line1", "line2", "line3"]
        assert event.data_str == "line1\nline2\nline3"

    def test_event_type(self) -> None:
        """event: lines should set the event type."""
        event = _SSEEvent()
        parse_sse_line("event: agent_status", event)
        assert event.event_type == "agent_status"

    def test_event_id(self) -> None:
        """id: lines should set the event ID."""
        event = _SSEEvent()
        parse_sse_line("id: evt-123", event)
        assert event.event_id == "evt-123"

    def test_event_id_with_null_rejected(self) -> None:
        """id: values containing null characters should be rejected."""
        event = _SSEEvent()
        parse_sse_line("id: bad\0id", event)
        assert event.event_id is None

    def test_retry_field(self) -> None:
        """retry: lines should set the retry interval."""
        event = _SSEEvent()
        parse_sse_line("retry: 5000", event)
        assert event.retry == 5000

    def test_retry_field_invalid(self) -> None:
        """Non-numeric retry values should be ignored."""
        event = _SSEEvent()
        parse_sse_line("retry: not-a-number", event)
        assert event.retry is None

    def test_unknown_field_ignored(self) -> None:
        """Unknown fields should be silently ignored."""
        event = _SSEEvent()
        parse_sse_line("custom: value", event)
        assert event.is_empty()
        assert event.event_type is None

    def test_field_with_no_value(self) -> None:
        """A field with no colon should be treated as field with empty value."""
        event = _SSEEvent()
        parse_sse_line("data", event)
        assert event.data == [""]


# ---------------------------------------------------------------------------
# Event data parsing tests
# ---------------------------------------------------------------------------


class TestParseEventData:
    """Tests for parsing SSE event data into typed models."""

    def test_parse_agent_event(self) -> None:
        event = _SSEEvent()
        event.data = [json.dumps({
            "type": "agent_status",
            "agentId": "agent-123",
            "status": "running",
            "message": "Agent started",
        })]
        result = _parse_event_data(event, AgentEvent)
        assert result is not None
        assert result.type == "agent_status"
        assert result.agent_id == "agent-123"
        assert result.status == "running"
        assert result.message == "Agent started"

    def test_parse_log_entry(self) -> None:
        event = _SSEEvent()
        event.data = [json.dumps({
            "type": "log_entry",
            "severity": "INFO",
            "message": "Processing request",
            "agentId": "agent-456",
        })]
        result = _parse_event_data(event, LogEntry)
        assert result is not None
        assert result.severity == "INFO"
        assert result.message == "Processing request"
        assert result.agent_id == "agent-456"

    def test_parse_empty_event_returns_none(self) -> None:
        event = _SSEEvent()
        result = _parse_event_data(event, AgentEvent)
        assert result is None

    def test_parse_invalid_json_returns_none(self) -> None:
        event = _SSEEvent()
        event.data = ["not valid json"]
        result = _parse_event_data(event, AgentEvent)
        assert result is None

    def test_parse_non_object_json_returns_none(self) -> None:
        event = _SSEEvent()
        event.data = ['"just a string"']
        result = _parse_event_data(event, AgentEvent)
        assert result is None

    def test_event_type_from_sse_header(self) -> None:
        """If JSON doesn't include type, the SSE event: header should be used."""
        event = _SSEEvent()
        event.event_type = "agent_status"
        event.data = [json.dumps({"agentId": "agent-123", "status": "running"})]
        result = _parse_event_data(event, AgentEvent)
        assert result is not None
        assert result.type == "agent_status"

    def test_json_type_takes_precedence(self) -> None:
        """If JSON includes type, it should override the SSE event: header."""
        event = _SSEEvent()
        event.event_type = "generic"
        event.data = [json.dumps({"type": "agent_status", "status": "running"})]
        result = _parse_event_data(event, AgentEvent)
        assert result is not None
        assert result.type == "agent_status"

    def test_multi_line_data_parsed(self) -> None:
        """Multi-line data should be joined with newlines before parsing."""
        event = _SSEEvent()
        event.data = ['{"type": "log_entry",', '"severity": "ERROR",', '"message": "fail"}']
        result = _parse_event_data(event, LogEntry)
        assert result is not None
        assert result.severity == "ERROR"
        assert result.message == "fail"

    def test_event_id_preserved(self) -> None:
        """The SSE event ID should be stored on the parsed model."""
        event = _SSEEvent()
        event.event_id = "evt-999"
        event.data = [json.dumps({"type": "agent_status", "status": "running"})]
        result = _parse_event_data(event, AgentEvent)
        assert result is not None
        assert result.id == "evt-999"


# ---------------------------------------------------------------------------
# Sync SSE Iterator tests
# ---------------------------------------------------------------------------


def _make_sync_response(lines: list[str], status_code: int = 200) -> httpx.Response:
    """Create a mock httpx.Response that yields the given lines from iter_lines()."""
    response = MagicMock(spec=httpx.Response)
    response.status_code = status_code
    response.iter_lines.return_value = iter(lines)
    response.close = MagicMock()
    return response


class TestSyncSSEIterator:
    """Tests for the synchronous SSE iterator."""

    def test_basic_event_iteration(self) -> None:
        """Simple event sequence should yield parsed models."""
        lines = [
            "event: agent_status",
            'data: {"type":"agent_status","agentId":"a1","status":"running"}',
            "",
            "event: agent_status",
            'data: {"type":"agent_status","agentId":"a1","status":"completed"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 2
        assert events[0].status == "running"
        assert events[1].status == "completed"

    def test_heartbeat_filtering(self) -> None:
        """Heartbeat lines should be skipped."""
        lines = [
            ":heartbeat",
            "event: agent_status",
            'data: {"type":"agent_status","status":"running"}',
            "",
            ":",
            ":heartbeat",
            "event: agent_status",
            'data: {"type":"agent_status","status":"completed"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 2
        assert events[0].status == "running"
        assert events[1].status == "completed"

    def test_multi_line_data_event(self) -> None:
        """Multi-line data fields should be aggregated."""
        # The JSON is split across multiple data: lines
        lines = [
            "event: log_entry",
            'data: {"type": "log_entry",',
            'data: "severity": "INFO",',
            'data: "message": "hello"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, LogEntry))

        assert len(events) == 1
        assert events[0].severity == "INFO"
        assert events[0].message == "hello"

    def test_context_manager(self) -> None:
        """Iterator should support context manager protocol."""
        lines = [
            'data: {"type":"agent_status","status":"running"}',
            "",
        ]
        response = _make_sync_response(lines)

        with SyncSSEIterator(response, AgentEvent) as stream:
            events = list(stream)

        assert len(events) == 1
        response.close.assert_called_once()

    def test_close_stops_iteration(self) -> None:
        """Closing the iterator mid-stream should stop yielding events."""
        lines = [
            'data: {"type":"agent_status","status":"running"}',
            "",
            'data: {"type":"agent_status","status":"completed"}',
            "",
        ]
        response = _make_sync_response(lines)

        collected: list[AgentEvent] = []
        with SyncSSEIterator(response, AgentEvent) as stream:
            for event in stream:
                collected.append(event)
                stream.close()  # Close after first event

        assert len(collected) == 1
        assert collected[0].status == "running"

    def test_last_event_id_tracked(self) -> None:
        """The iterator should track the last event ID for reconnection."""
        lines = [
            "id: evt-1",
            'data: {"type":"agent_status","status":"running"}',
            "",
            "id: evt-2",
            'data: {"type":"agent_status","status":"completed"}',
            "",
        ]
        response = _make_sync_response(lines)
        stream = SyncSSEIterator(response, AgentEvent)
        events = list(stream)

        assert len(events) == 2
        assert stream.last_event_id == "evt-2"

    def test_last_event_id_initial_value(self) -> None:
        """The iterator should accept an initial last_event_id for reconnection."""
        response = _make_sync_response([])
        stream = SyncSSEIterator(response, AgentEvent, last_event_id="evt-0")
        assert stream.last_event_id == "evt-0"

    def test_empty_stream(self) -> None:
        """An empty stream should yield no events."""
        response = _make_sync_response([])
        events = list(SyncSSEIterator(response, AgentEvent))
        assert events == []

    def test_only_heartbeats(self) -> None:
        """A stream with only heartbeats should yield no events."""
        lines = [":heartbeat", ":", ":keepalive"]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))
        assert events == []

    def test_invalid_json_skipped(self) -> None:
        """Events with invalid JSON data should be skipped."""
        lines = [
            "data: not-json",
            "",
            'data: {"type":"agent_status","status":"running"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 1
        assert events[0].status == "running"

    def test_event_without_data_skipped(self) -> None:
        """Events with only event type but no data should be skipped."""
        lines = [
            "event: ping",
            "",
            'data: {"type":"agent_status","status":"running"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 1

    def test_final_event_without_trailing_blank_line(self) -> None:
        """An event at end-of-stream without trailing blank line should be yielded."""
        lines = [
            'data: {"type":"agent_status","status":"running"}',
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 1
        assert events[0].status == "running"

    def test_stream_read_error(self) -> None:
        """httpx.ReadError during streaming should raise StreamError."""
        response = MagicMock(spec=httpx.Response)

        def raise_read_error():
            raise httpx.ReadError("connection reset")
            yield  # pragma: no cover - make it a generator

        response.iter_lines.return_value = raise_read_error()
        response.close = MagicMock()

        from scion._errors import StreamError

        with pytest.raises(StreamError, match="Stream read error"):
            list(SyncSSEIterator(response, AgentEvent))

    def test_stream_protocol_error(self) -> None:
        """httpx.RemoteProtocolError during streaming should raise StreamError."""
        response = MagicMock(spec=httpx.Response)

        def raise_protocol_error():
            raise httpx.RemoteProtocolError("invalid chunk")
            yield  # pragma: no cover - make it a generator

        response.iter_lines.return_value = raise_protocol_error()
        response.close = MagicMock()

        from scion._errors import StreamError

        with pytest.raises(StreamError, match="Stream protocol error"):
            list(SyncSSEIterator(response, AgentEvent))


# ---------------------------------------------------------------------------
# Async SSE Iterator tests
# ---------------------------------------------------------------------------


class _AsyncLineIterator:
    """Helper that wraps a list of lines into an async iterator."""

    def __init__(self, lines: list[str]) -> None:
        self._lines = iter(lines)

    def __aiter__(self) -> _AsyncLineIterator:
        return self

    async def __anext__(self) -> str:
        try:
            return next(self._lines)
        except StopIteration:
            raise StopAsyncIteration from None


def _make_async_response(lines: list[str], status_code: int = 200) -> httpx.Response:
    """Create a mock httpx.Response that yields the given lines from aiter_lines()."""
    response = MagicMock(spec=httpx.Response)
    response.status_code = status_code
    response.aiter_lines.return_value = _AsyncLineIterator(lines)
    response.aclose = AsyncMock()
    return response


class TestAsyncSSEIterator:
    """Tests for the asynchronous SSE iterator."""

    @pytest.mark.asyncio
    async def test_basic_event_iteration(self) -> None:
        lines = [
            "event: agent_status",
            'data: {"type":"agent_status","agentId":"a1","status":"running"}',
            "",
            "event: agent_status",
            'data: {"type":"agent_status","agentId":"a1","status":"completed"}',
            "",
        ]
        response = _make_async_response(lines)
        events = [e async for e in AsyncSSEIterator(response, AgentEvent)]

        assert len(events) == 2
        assert events[0].status == "running"
        assert events[1].status == "completed"

    @pytest.mark.asyncio
    async def test_heartbeat_filtering(self) -> None:
        lines = [
            ":heartbeat",
            "event: agent_status",
            'data: {"type":"agent_status","status":"running"}',
            "",
            ":",
            "event: agent_status",
            'data: {"type":"agent_status","status":"completed"}',
            "",
        ]
        response = _make_async_response(lines)
        events = [e async for e in AsyncSSEIterator(response, AgentEvent)]

        assert len(events) == 2
        assert events[0].status == "running"
        assert events[1].status == "completed"

    @pytest.mark.asyncio
    async def test_multi_line_data(self) -> None:
        lines = [
            "event: log_entry",
            'data: {"type": "log_entry",',
            'data: "severity": "ERROR",',
            'data: "message": "oops"}',
            "",
        ]
        response = _make_async_response(lines)
        events = [e async for e in AsyncSSEIterator(response, LogEntry)]

        assert len(events) == 1
        assert events[0].severity == "ERROR"
        assert events[0].message == "oops"

    @pytest.mark.asyncio
    async def test_async_context_manager(self) -> None:
        lines = [
            'data: {"type":"agent_status","status":"running"}',
            "",
        ]
        response = _make_async_response(lines)

        async with AsyncSSEIterator(response, AgentEvent) as stream:
            events = [e async for e in stream]

        assert len(events) == 1
        response.aclose.assert_called_once()

    @pytest.mark.asyncio
    async def test_close_stops_iteration(self) -> None:
        lines = [
            'data: {"type":"agent_status","status":"running"}',
            "",
            'data: {"type":"agent_status","status":"completed"}',
            "",
        ]
        response = _make_async_response(lines)

        collected: list[AgentEvent] = []
        async with AsyncSSEIterator(response, AgentEvent) as stream:
            async for event in stream:
                collected.append(event)
                await stream.close()  # Close after first event
                break

        assert len(collected) == 1
        assert collected[0].status == "running"

    @pytest.mark.asyncio
    async def test_last_event_id_tracked(self) -> None:
        lines = [
            "id: evt-1",
            'data: {"type":"agent_status","status":"running"}',
            "",
            "id: evt-2",
            'data: {"type":"agent_status","status":"completed"}',
            "",
        ]
        response = _make_async_response(lines)
        stream = AsyncSSEIterator(response, AgentEvent)
        events = [e async for e in stream]

        assert len(events) == 2
        assert stream.last_event_id == "evt-2"

    @pytest.mark.asyncio
    async def test_empty_stream(self) -> None:
        response = _make_async_response([])
        events = [e async for e in AsyncSSEIterator(response, AgentEvent)]
        assert events == []

    @pytest.mark.asyncio
    async def test_only_heartbeats(self) -> None:
        lines = [":heartbeat", ":", ":keepalive"]
        response = _make_async_response(lines)
        events = [e async for e in AsyncSSEIterator(response, AgentEvent)]
        assert events == []

    @pytest.mark.asyncio
    async def test_invalid_json_skipped(self) -> None:
        lines = [
            "data: {bad json}",
            "",
            'data: {"type":"agent_status","status":"running"}',
            "",
        ]
        response = _make_async_response(lines)
        events = [e async for e in AsyncSSEIterator(response, AgentEvent)]

        assert len(events) == 1
        assert events[0].status == "running"

    @pytest.mark.asyncio
    async def test_stream_read_error(self) -> None:
        """httpx.ReadError during async streaming should raise StreamError."""
        response = MagicMock(spec=httpx.Response)

        class _ErrorIter:
            def __aiter__(self) -> _ErrorIter:
                return self

            async def __anext__(self) -> str:
                raise httpx.ReadError("connection lost")

        response.aiter_lines.return_value = _ErrorIter()
        response.aclose = AsyncMock()

        from scion._errors import StreamError

        with pytest.raises(StreamError, match="Stream read error"):
            async for _ in AsyncSSEIterator(response, AgentEvent):
                pass  # pragma: no cover


# ---------------------------------------------------------------------------
# StreamEvent type model tests
# ---------------------------------------------------------------------------


class TestStreamEventTypes:
    """Tests for the streaming event Pydantic models."""

    def test_agent_event_from_json(self) -> None:
        data = {
            "type": "agent_status",
            "agentId": "agent-123",
            "status": "running",
            "phase": "active",
            "message": "Agent is running",
            "timestamp": "2026-05-12T10:30:00Z",
        }
        event = AgentEvent.model_validate(data)
        assert event.type == "agent_status"
        assert event.agent_id == "agent-123"
        assert event.status == "running"
        assert event.phase == "active"
        assert event.message == "Agent is running"
        assert event.timestamp is not None

    def test_log_entry_from_json(self) -> None:
        data = {
            "type": "log_entry",
            "severity": "ERROR",
            "message": "Something went wrong",
            "agentId": "agent-456",
            "source": "harness",
            "timestamp": "2026-05-12T10:30:00Z",
        }
        entry = LogEntry.model_validate(data)
        assert entry.severity == "ERROR"
        assert entry.message == "Something went wrong"
        assert entry.agent_id == "agent-456"
        assert entry.source == "harness"

    def test_stream_event_base(self) -> None:
        data = {"type": "unknown_event"}
        event = StreamEvent.model_validate(data)
        assert event.type == "unknown_event"
        assert event.id is None

    def test_agent_event_extra_fields_ignored(self) -> None:
        """Extra fields in the JSON payload should be ignored."""
        data = {
            "type": "agent_status",
            "agentId": "a1",
            "unknownField": "ignored",
        }
        event = AgentEvent.model_validate(data)
        assert event.agent_id == "a1"

    def test_log_entry_minimal(self) -> None:
        """LogEntry should work with minimal fields."""
        data = {"type": "log_entry"}
        entry = LogEntry.model_validate(data)
        assert entry.type == "log_entry"
        assert entry.severity is None
        assert entry.message is None

    def test_agent_event_with_data_dict(self) -> None:
        data = {
            "type": "agent_status",
            "data": {"cpu": 85.2, "memory": "1.2GB"},
        }
        event = AgentEvent.model_validate(data)
        assert event.data == {"cpu": 85.2, "memory": "1.2GB"}


# ---------------------------------------------------------------------------
# Full event sequence simulation
# ---------------------------------------------------------------------------


class TestFullEventSequence:
    """End-to-end tests simulating realistic SSE streams."""

    def test_realistic_agent_stream(self) -> None:
        """Simulate a realistic agent event stream with mixed event types."""
        lines = [
            ":heartbeat",
            "",
            "event: agent_status",
            'data: {"type":"agent_status","agentId":"a1","status":"starting","message":"Agent provisioning"}',
            "",
            ":heartbeat",
            "",
            "event: agent_status",
            "id: evt-2",
            'data: {"type":"agent_status","agentId":"a1","status":"running","message":"Agent started"}',
            "",
            ":heartbeat",
            "",
            "event: agent_status",
            "id: evt-3",
            'data: {"type":"agent_status","agentId":"a1","status":"completed","message":"Task done"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 3
        assert events[0].status == "starting"
        assert events[1].status == "running"
        assert events[2].status == "completed"
        assert events[2].message == "Task done"

    def test_realistic_log_stream(self) -> None:
        """Simulate a realistic cloud log stream."""
        lines = [
            "event: log_entry",
            'data: {"type":"log_entry","severity":"INFO","message":"Starting build","agentId":"a1"}',
            "",
            ":heartbeat",
            "",
            "event: log_entry",
            'data: {"type":"log_entry","severity":"WARNING","message":"Deprecated API used","agentId":"a1"}',
            "",
            "event: log_entry",
            'data: {"type":"log_entry","severity":"ERROR","message":"Build failed","agentId":"a1"}',
            "",
        ]
        response = _make_sync_response(lines)
        entries = list(SyncSSEIterator(response, LogEntry))

        assert len(entries) == 3
        assert entries[0].severity == "INFO"
        assert entries[1].severity == "WARNING"
        assert entries[2].severity == "ERROR"

    @pytest.mark.asyncio
    async def test_realistic_async_stream(self) -> None:
        """Simulate a realistic async agent event stream."""
        lines = [
            ":heartbeat",
            "",
            "event: agent_status",
            "id: 1",
            'data: {"type":"agent_status","agentId":"a1","status":"running"}',
            "",
            ":heartbeat",
            "",
            "event: agent_status",
            "id: 2",
            'data: {"type":"agent_status","agentId":"a1","status":"completed"}',
            "",
        ]
        response = _make_async_response(lines)

        collected = []
        async with AsyncSSEIterator(response, AgentEvent) as stream:
            async for event in stream:
                collected.append(event)

        assert len(collected) == 2
        assert stream.last_event_id == "2"

    def test_break_out_of_iteration(self) -> None:
        """Breaking out of iteration should work cleanly."""
        lines = [
            'data: {"type":"agent_status","status":"running"}',
            "",
            'data: {"type":"agent_status","status":"completed"}',
            "",
            'data: {"type":"agent_status","status":"after_break"}',
            "",
        ]
        response = _make_sync_response(lines)

        collected = []
        with SyncSSEIterator(response, AgentEvent) as stream:
            for event in stream:
                collected.append(event)
                if event.status == "completed":
                    break

        assert len(collected) == 2
        assert collected[-1].status == "completed"
        response.close.assert_called()

    def test_consecutive_heartbeats(self) -> None:
        """Multiple consecutive heartbeats should not produce events."""
        lines = [
            ":heartbeat",
            ":heartbeat",
            ":heartbeat",
            "",
            "",
            ":heartbeat",
            'data: {"type":"agent_status","status":"running"}',
            "",
        ]
        response = _make_sync_response(lines)
        events = list(SyncSSEIterator(response, AgentEvent))

        assert len(events) == 1
        assert events[0].status == "running"

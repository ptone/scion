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

"""SSE (Server-Sent Events) streaming support for the Scion Python SDK.

Provides sync and async iterators that parse SSE wire format and yield
typed Pydantic models. Supports:
- SSE line parsing (``data:``, ``event:``, ``id:`` fields)
- Heartbeat filtering (``:heartbeat`` or bare ``:`` comments)
- Multi-line ``data:`` aggregation
- Context manager for clean connection teardown
- Optional auto-reconnect with ``Last-Event-ID``
"""

from __future__ import annotations

import contextlib
import json
import logging
from collections.abc import Iterator
from typing import Generic, TypeVar

import httpx

from scion._errors import StreamError
from scion.types.common import _ScionModel

logger = logging.getLogger(__name__)

T = TypeVar("T", bound=_ScionModel)


class _SSEEvent:
    """Internal representation of a parsed SSE event before model conversion."""

    __slots__ = ("event_type", "data", "event_id", "retry")

    def __init__(self) -> None:
        self.event_type: str | None = None
        self.data: list[str] = []
        self.event_id: str | None = None
        self.retry: int | None = None

    @property
    def data_str(self) -> str:
        """Join multi-line data fields with newlines (per SSE spec)."""
        return "\n".join(self.data)

    def is_empty(self) -> bool:
        """Return True if no data was collected."""
        return len(self.data) == 0


def parse_sse_line(line: str, event: _SSEEvent) -> bool:
    """Parse a single SSE line into the current event buffer.

    Returns True if the line was an empty line (event boundary), signaling
    that the accumulated event should be dispatched.

    SSE wire format rules:
    - Empty line = dispatch current event
    - Lines starting with ``:`` = comment (heartbeat), skip
    - ``data: <payload>`` = append payload to event data
    - ``event: <type>`` = set event type
    - ``id: <value>`` = set event ID (for reconnection)
    - ``retry: <ms>`` = set reconnection interval (advisory)
    """
    # Empty line signals end of event
    if not line:
        return True

    # Comment line (heartbeat or annotation) — skip
    if line.startswith(":"):
        return False

    # Split on first colon
    if ":" in line:
        field, _, value = line.partition(":")
        # Strip single leading space from value (per SSE spec)
        if value.startswith(" "):
            value = value[1:]
    else:
        # Field with no value
        field = line
        value = ""

    if field == "data":
        event.data.append(value)
    elif field == "event":
        event.event_type = value
    elif field == "id":
        # Per SSE spec, id must not contain null
        if "\0" not in value:
            event.event_id = value
    elif field == "retry":
        with contextlib.suppress(ValueError):
            event.retry = int(value)
    # Unknown fields are ignored per spec

    return False


def _parse_event_data(event: _SSEEvent, model_class: type[T]) -> T | None:
    """Parse an SSE event's data into a typed model.

    Returns None if the data is empty or cannot be parsed.
    """
    if event.is_empty():
        return None

    data_str = event.data_str
    try:
        data_dict = json.loads(data_str)
    except json.JSONDecodeError:
        logger.warning("Failed to parse SSE event data as JSON: %s", data_str[:200])
        return None

    if not isinstance(data_dict, dict):
        logger.warning("SSE event data is not a JSON object: %s", type(data_dict))
        return None

    try:
        obj = model_class.model_validate(data_dict)
        # Set the event type from the SSE event: header if present and not
        # already set in the JSON payload.
        if event.event_type and hasattr(obj, "type") and not data_dict.get("type"):
            obj.type = event.event_type
        # Preserve the event ID for reconnection
        if event.event_id and hasattr(obj, "id"):
            obj.id = event.event_id
        # Store raw data for debugging
        if hasattr(obj, "raw_data"):
            obj.raw_data = data_str
        return obj
    except Exception:
        logger.warning("Failed to validate SSE event data against %s", model_class.__name__)
        return None


class SyncSSEIterator(Generic[T]):
    """Synchronous iterator over SSE events from an httpx streaming response.

    Parses the SSE wire format and yields typed Pydantic model instances.
    Supports use as a context manager for clean connection teardown.

    Usage::

        with SyncSSEIterator(response, AgentEvent) as stream:
            for event in stream:
                print(event.type, event.message)
    """

    def __init__(
        self,
        response: httpx.Response,
        model_class: type[T],
        *,
        last_event_id: str | None = None,
    ) -> None:
        self._response = response
        self._model_class = model_class
        self._last_event_id = last_event_id
        self._closed = False

    @property
    def last_event_id(self) -> str | None:
        """The ID of the last successfully received event.

        Can be used for reconnection via the ``Last-Event-ID`` header.
        """
        return self._last_event_id

    def __enter__(self) -> SyncSSEIterator[T]:
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

    def __iter__(self) -> Iterator[T]:
        return self._iterate()

    def _iterate(self) -> Iterator[T]:
        """Parse SSE lines from the response and yield typed events."""
        current_event = _SSEEvent()

        try:
            for line in self._response.iter_lines():
                if self._closed:
                    break

                is_boundary = parse_sse_line(line, current_event)

                if is_boundary:
                    obj = _parse_event_data(current_event, self._model_class)
                    if obj is not None:
                        # Track last event ID for reconnection
                        if current_event.event_id:
                            self._last_event_id = current_event.event_id
                        yield obj
                    # Reset for next event
                    current_event = _SSEEvent()

            # Handle final event if stream ends without trailing blank line
            if not current_event.is_empty():
                obj = _parse_event_data(current_event, self._model_class)
                if obj is not None:
                    if current_event.event_id:
                        self._last_event_id = current_event.event_id
                    yield obj

        except httpx.RemoteProtocolError as exc:
            raise StreamError(f"Stream protocol error: {exc}") from exc
        except httpx.ReadError as exc:
            raise StreamError(f"Stream read error: {exc}") from exc

    def close(self) -> None:
        """Close the underlying streaming response."""
        if not self._closed:
            self._closed = True
            self._response.close()


class AsyncSSEIterator(Generic[T]):
    """Asynchronous iterator over SSE events from an httpx async streaming response.

    Parses the SSE wire format and yields typed Pydantic model instances.
    Supports use as an async context manager for clean connection teardown.

    Usage::

        async with AsyncSSEIterator(response, AgentEvent) as stream:
            async for event in stream:
                print(event.type, event.message)
    """

    def __init__(
        self,
        response: httpx.Response,
        model_class: type[T],
        *,
        last_event_id: str | None = None,
    ) -> None:
        self._response = response
        self._model_class = model_class
        self._last_event_id = last_event_id
        self._closed = False

    @property
    def last_event_id(self) -> str | None:
        """The ID of the last successfully received event.

        Can be used for reconnection via the ``Last-Event-ID`` header.
        """
        return self._last_event_id

    async def __aenter__(self) -> AsyncSSEIterator[T]:
        return self

    async def __aexit__(self, *args: object) -> None:
        await self.close()

    def __aiter__(self) -> AsyncSSEIterator[T]:
        return self

    async def __anext__(self) -> T:
        """Get the next typed event from the stream."""
        # We use a stateful approach: buffer lines and dispatch on boundaries
        if not hasattr(self, "_line_iter"):
            self._line_iter = self._response.aiter_lines()
            self._current_event = _SSEEvent()

        while True:
            if self._closed:
                raise StopAsyncIteration

            try:
                line = await self._line_iter.__anext__()
            except StopAsyncIteration:
                # Stream ended — check for any remaining buffered event
                if not self._current_event.is_empty():
                    obj = _parse_event_data(self._current_event, self._model_class)
                    self._current_event = _SSEEvent()
                    if obj is not None:
                        return obj
                raise
            except httpx.RemoteProtocolError as exc:
                raise StreamError(f"Stream protocol error: {exc}") from exc
            except httpx.ReadError as exc:
                raise StreamError(f"Stream read error: {exc}") from exc

            is_boundary = parse_sse_line(line, self._current_event)

            if is_boundary:
                obj = _parse_event_data(self._current_event, self._model_class)
                if obj is not None:
                    if self._current_event.event_id:
                        self._last_event_id = self._current_event.event_id
                    self._current_event = _SSEEvent()
                    return obj
                # Empty event or parse failure — reset and continue
                self._current_event = _SSEEvent()

    async def close(self) -> None:
        """Close the underlying streaming response."""
        if not self._closed:
            self._closed = True
            await self._response.aclose()

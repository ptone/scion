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

"""Streaming event types for the Scion Python SDK.

Defines typed models for SSE (Server-Sent Events) streaming responses
from the Scion Hub API, including agent events and log entries.
"""

from __future__ import annotations

from datetime import datetime

from pydantic import Field

from scion.types.common import _ScionModel


class StreamEvent(_ScionModel):
    """Base model for all SSE stream events.

    Every event from an SSE endpoint carries at minimum a ``type``
    discriminator and the raw JSON ``data`` payload.
    """

    type: str = ""
    """The event type discriminator (e.g. ``"agent_status"``, ``"log_entry"``)."""

    id: str | None = None
    """Optional event ID for reconnection (``Last-Event-ID``)."""

    raw_data: str | None = Field(None, exclude=True)
    """The raw JSON string before parsing, excluded from serialization."""


class AgentEvent(StreamEvent):
    """An agent lifecycle or status event from the messages stream.

    Received from ``GET /api/v1/agents/{id}/messages/stream``.
    """

    agent_id: str = Field("", alias="agentId")
    """The agent this event pertains to."""

    status: str | None = None
    """Current agent status (e.g. ``"running"``, ``"completed"``, ``"failed"``)."""

    phase: str | None = None
    """Current agent lifecycle phase."""

    message: str | None = None
    """Human-readable event description."""

    timestamp: datetime | None = None
    """When the event occurred."""

    data: dict | None = None
    """Additional structured data attached to the event."""


class LogEntry(StreamEvent):
    """A structured log entry from cloud log streaming.

    Received from ``GET /api/v1/agents/{id}/cloud-logs/stream``
    or ``GET /api/v1/projects/{id}/cloud-logs/stream``.
    """

    timestamp: datetime | None = None
    """When the log entry was recorded."""

    severity: str | None = None
    """Log severity level (e.g. ``"INFO"``, ``"WARNING"``, ``"ERROR"``)."""

    message: str | None = None
    """The log message text."""

    agent_id: str | None = Field(None, alias="agentId")
    """The agent that produced this log entry."""

    source: str | None = None
    """The log source or component name."""

    data: dict | None = None
    """Additional structured data attached to the log entry."""

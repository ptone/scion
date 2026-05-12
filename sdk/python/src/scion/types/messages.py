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

"""Message type models for the Scion Python SDK.

Mirrors Go types in ``pkg/hubclient/messages.go``.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any

from pydantic import ConfigDict, Field

from scion.types.common import _ScionModel


class StructuredMessage(_ScionModel):
    """A structured message payload for agent communication.

    Used with :meth:`AgentsResource.send_structured_message` and
    :meth:`AgentsResource.broadcast_message` to deliver typed,
    machine-readable messages to agents.

    Attributes:
        type: Message type (e.g. ``"instruction"``, ``"input-needed"``).
        sender: Sender identity string (e.g. ``"user:alice"``).
        sender_id: Sender UUID.
        content: Human-readable message content.
        data: Arbitrary structured data payload.
        urgent: Mark the message as urgent.
    """

    type: str = ""
    sender: str | None = None
    sender_id: str | None = Field(None, alias="senderId")
    content: str | None = None
    data: dict[str, Any] | None = None
    urgent: bool | None = None

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        ser_json_by_alias=True,
    )

    def model_dump_api(self) -> dict[str, Any]:
        """Dump model for API requests, using camelCase aliases and excluding None."""
        return self.model_dump(by_alias=True, exclude_none=True)


class Message(_ScionModel):
    """A message from the user's inbox.

    Represents a single message received by the authenticated user,
    including metadata about sender, recipient, and read state.

    Attributes:
        id: Message UUID.
        project_id: Project ID this message relates to.
        sender: Display name of the sender.
        sender_id: Sender user/agent ID.
        recipient: Display name of the recipient.
        recipient_id: Recipient user/agent ID.
        msg: Message body text.
        type: Message type (e.g. ``"instruction"``, ``"state-change"``).
        urgent: Whether this message is urgent.
        broadcasted: Whether this message was broadcasted.
        read: Whether this message has been read.
        agent_id: Agent ID if this message is from/to an agent.
        created_at: Creation timestamp.
    """

    id: str = ""
    project_id: str | None = Field(None, alias="projectId")
    sender: str | None = None
    sender_id: str | None = Field(None, alias="senderId")
    recipient: str | None = None
    recipient_id: str | None = Field(None, alias="recipientId")
    msg: str = ""
    type: str = ""
    urgent: bool | None = None
    broadcasted: bool | None = None
    read: bool = False
    agent_id: str | None = Field(None, alias="agentId")
    created_at: datetime | None = Field(None, alias="createdAt")


class AgentMessage(_ScionModel):
    """A lightweight message view used in agent-scoped listings."""

    id: str = ""
    project_id: str | None = Field(None, alias="projectId")
    sender: str | None = None
    sender_id: str | None = Field(None, alias="senderId")
    recipient: str | None = None
    recipient_id: str | None = Field(None, alias="recipientId")
    msg: str = ""
    type: str = ""
    urgent: bool | None = None
    broadcasted: bool | None = None
    read: bool = False
    agent_id: str | None = Field(None, alias="agentId")
    created_at: datetime | None = Field(None, alias="createdAt")

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

"""Agent type models for the Scion Python SDK.

Mirrors Go types in ``pkg/hubclient/types.go`` and ``pkg/hubclient/agents.go``.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any

from pydantic import ConfigDict, Field

from scion.types.common import ListMeta, _ScionModel


class AgentConfig(_ScionModel):
    """Agent configuration."""

    image: str | None = None
    harness_config: str | None = Field(None, alias="harnessConfig")
    harness_auth: str | None = Field(None, alias="harnessAuth")
    env: dict[str, str] | None = None
    model: str | None = None
    profile: str | None = None
    task: str | None = None


class DirectConnect(_ScionModel):
    """Direct connection info for an agent."""

    enabled: bool = False
    ssh_host: str | None = Field(None, alias="sshHost")
    ssh_port: int | None = Field(None, alias="sshPort")
    ssh_user: str | None = Field(None, alias="sshUser")


class Agent(_ScionModel):
    """An agent from the Hub API."""

    id: str = ""
    slug: str = ""
    container_id: str = Field("", alias="containerId")
    name: str = ""
    template: str | None = None
    harness_config: str | None = Field(None, alias="harnessConfig")
    harness_auth: str | None = Field(None, alias="harnessAuth")
    project_id: str | None = Field(None, alias="projectId")
    project: str | None = None
    labels: dict[str, str] | None = None
    annotations: dict[str, str] | None = None
    phase: str | None = None
    activity: str | None = None
    status: str = ""
    connection_state: str | None = Field(None, alias="connectionState")
    container_status: str | None = Field(None, alias="containerStatus")
    runtime_state: str | None = Field(None, alias="runtimeState")
    image: str | None = None
    detached: bool | None = None
    runtime: str | None = None
    runtime_broker_id: str | None = Field(None, alias="runtimeBrokerId")
    runtime_broker_name: str | None = Field(None, alias="runtimeBrokerName")
    runtime_broker_type: str | None = Field(None, alias="runtimeBrokerType")
    web_pty_enabled: bool | None = Field(None, alias="webPtyEnabled")
    task_summary: str | None = Field(None, alias="taskSummary")
    applied_config: AgentConfig | None = Field(None, alias="appliedConfig")
    direct_connect: DirectConnect | None = Field(None, alias="directConnect")
    created: datetime | None = None
    updated: datetime | None = None
    last_seen: datetime | None = Field(None, alias="lastSeen")
    deleted_at: datetime | None = Field(None, alias="deletedAt")
    created_by: str | None = Field(None, alias="createdBy")
    owner_id: str | None = Field(None, alias="ownerId")
    visibility: str | None = None
    state_version: int | None = Field(None, alias="stateVersion")


class CreateAgentRequest(_ScionModel):
    """Request body for creating an agent."""

    name: str
    project_id: str = Field(alias="projectId")
    template: str | None = None
    harness_config: str | None = Field(None, alias="harnessConfig")
    harness_auth: str | None = Field(None, alias="harnessAuth")
    runtime_broker_id: str | None = Field(None, alias="runtimeBrokerId")
    profile: str | None = None
    task: str | None = None
    branch: str | None = None
    workspace: str | None = None
    labels: dict[str, str] | None = None
    annotations: dict[str, str] | None = None
    resume: bool | None = None
    attach: bool | None = None
    provision_only: bool | None = Field(None, alias="provisionOnly")
    notify: bool | None = None

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        ser_json_by_alias=True,
    )

    def model_dump_api(self) -> dict[str, Any]:
        """Dump model for API requests, using camelCase aliases and excluding None."""
        return self.model_dump(by_alias=True, exclude_none=True)


class CreateAgentResponse(_ScionModel):
    """Response from creating an agent."""

    agent: Agent | None = None
    warnings: list[str] | None = None


class UpdateAgentRequest(_ScionModel):
    """Request body for updating an agent."""

    name: str | None = None
    labels: dict[str, str] | None = None
    annotations: dict[str, str] | None = None
    task_summary: str | None = Field(None, alias="taskSummary")
    state_version: int = Field(alias="stateVersion")

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        ser_json_by_alias=True,
    )

    def model_dump_api(self) -> dict[str, Any]:
        """Dump model for API requests, using camelCase aliases and excluding None."""
        return self.model_dump(by_alias=True, exclude_none=True)


class ListAgentsResponse(_ScionModel):
    """Response from listing agents."""

    agents: list[Agent] = Field(default_factory=list)
    meta: ListMeta = Field(default_factory=ListMeta)

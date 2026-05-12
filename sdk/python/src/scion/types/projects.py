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

"""Project type models for the Scion Python SDK.

Mirrors Go types in ``pkg/hubclient/types.go`` and ``pkg/hubclient/projects.go``.
"""

from __future__ import annotations

from datetime import datetime
from typing import Any

from pydantic import ConfigDict, Field

from scion.types.common import ListMeta, _ScionModel


class ProjectProvider(_ScionModel):
    """A broker providing runtime services to a project."""

    broker_id: str = Field(alias="brokerId")
    broker_name: str = Field("", alias="brokerName")
    status: str = ""
    last_seen: datetime | None = Field(None, alias="lastSeen")
    local_path: str | None = Field(None, alias="localPath")
    linked_by: str | None = Field(None, alias="linkedBy")
    linked_at: datetime | None = Field(None, alias="linkedAt")


class Project(_ScionModel):
    """A project from the Hub API.

    Projects are the primary organizational unit in Scion, grouping agents,
    templates, and runtime broker providers around a shared codebase.

    Attributes:
        id: Hub UUID.
        name: Human-readable name.
        slug: URL-safe slug.
        git_remote: Git remote URL associated with this project.
        default_runtime_broker_id: Default runtime broker ID.
        created: Creation timestamp.
        updated: Last-updated timestamp.
        visibility: Visibility level (``"private"``, ``"public"``).
        labels: User-defined labels.
        annotations: User-defined annotations.
        providers: Runtime brokers providing services to this project.
        agent_count: Number of agents in this project.
    """

    id: str = ""
    name: str = ""
    slug: str = ""
    git_remote: str | None = Field(None, alias="gitRemote")
    default_runtime_broker_id: str | None = Field(None, alias="defaultRuntimeBrokerId")
    created: datetime | None = None
    updated: datetime | None = None
    created_by: str | None = Field(None, alias="createdBy")
    owner_id: str | None = Field(None, alias="ownerId")
    visibility: str | None = None
    labels: dict[str, str] | None = None
    annotations: dict[str, str] | None = None
    providers: list[ProjectProvider] | None = None
    agent_count: int | None = Field(None, alias="agentCount")
    active_broker_count: int | None = Field(None, alias="activeBrokerCount")
    project_type: str | None = Field(None, alias="projectType")


class CreateProjectRequest(_ScionModel):
    """Request body for creating a project."""

    name: str
    git_remote: str | None = Field(None, alias="gitRemote")
    visibility: str | None = None
    labels: dict[str, str] | None = None
    slug: str | None = None
    id: str | None = None

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        ser_json_by_alias=True,
    )

    def model_dump_api(self) -> dict[str, Any]:
        """Dump model for API requests, using camelCase aliases and excluding None."""
        return self.model_dump(by_alias=True, exclude_none=True)


class UpdateProjectRequest(_ScionModel):
    """Request body for updating a project."""

    name: str | None = None
    labels: dict[str, str] | None = None
    annotations: dict[str, str] | None = None
    visibility: str | None = None
    default_runtime_broker_id: str | None = Field(None, alias="defaultRuntimeBrokerId")

    model_config = ConfigDict(
        populate_by_name=True,
        extra="ignore",
        ser_json_by_alias=True,
    )

    def model_dump_api(self) -> dict[str, Any]:
        """Dump model for API requests, using camelCase aliases and excluding None."""
        return self.model_dump(by_alias=True, exclude_none=True)


class ListProjectsResponse(_ScionModel):
    """Response from listing projects."""

    projects: list[Project] = Field(default_factory=list)
    meta: ListMeta = Field(default_factory=ListMeta)

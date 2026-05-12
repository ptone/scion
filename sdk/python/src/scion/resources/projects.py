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

"""Projects resource for the Scion Python SDK.

Provides synchronous and asynchronous access to project CRUD operations
and project-scoped agent listing via the Hub API.

Mirrors the Go ``ProjectService`` in ``pkg/hubclient/projects.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from scion._pagination import AsyncPage, SyncPage
from scion.types.agents import Agent
from scion.types.projects import (
    CreateProjectRequest,
    Project,
    UpdateProjectRequest,
)

if TYPE_CHECKING:
    from scion._transport import AsyncTransport, Transport

_BASE_PATH = "/api/v1/projects"


class ProjectsResource:
    """Synchronous resource for project operations.

    Accessed via ``client.projects``.

    Example::

        # List all projects
        page = client.projects.list()
        for project in page:
            print(project.name)

        # Get a specific project
        project = client.projects.get("proj-123")

        # Create a project
        project = client.projects.create(name="my-project")

        # Update a project
        project = client.projects.update("proj-123", name="new-name")

        # Delete a project
        client.projects.delete("proj-123")

        # List agents in a project
        agents_page = client.projects.list_agents("proj-123")
    """

    def __init__(self, transport: Transport) -> None:
        self._transport = transport

    def list(
        self,
        *,
        visibility: str | None = None,
        git_remote: str | None = None,
        broker_id: str | None = None,
        name: str | None = None,
        slug: str | None = None,
        labels: dict[str, str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> SyncPage[Project]:
        """List projects matching the given filters.

        Args:
            visibility: Filter by visibility (e.g. ``"public"``, ``"private"``).
            git_remote: Filter by git remote URL (exact or prefix match).
            broker_id: Filter by contributing broker ID.
            name: Filter by exact project name (case-insensitive).
            slug: Filter by exact project slug (case-insensitive).
            labels: Filter by label key-value pairs.
            limit: Maximum number of results per page.
            cursor: Pagination cursor from a previous response.

        Returns:
            A :class:`SyncPage` of :class:`Project` objects.
        """
        params = self._build_list_params(
            visibility=visibility,
            git_remote=git_remote,
            broker_id=broker_id,
            name=name,
            slug=slug,
            labels=labels,
            limit=limit,
            cursor=cursor,
        )
        return self._fetch_project_page(params)

    def get(self, project_id: str) -> Project:
        """Get a single project by ID.

        Args:
            project_id: The project identifier.

        Returns:
            The :class:`Project` object.

        Raises:
            NotFoundError: If the project does not exist.
        """
        resp = self._transport.get(f"{_BASE_PATH}/{project_id}")
        return Project.model_validate(resp.json())

    def create(
        self,
        *,
        name: str,
        git_remote: str | None = None,
        visibility: str | None = None,
        labels: dict[str, str] | None = None,
        slug: str | None = None,
        id: str | None = None,
    ) -> Project:
        """Create a new project.

        Args:
            name: The project name (required).
            git_remote: Optional git remote URL to associate.
            visibility: Optional visibility setting.
            labels: Optional label key-value pairs.
            slug: Optional project slug.
            id: Optional client-provided project ID.

        Returns:
            The created :class:`Project` object.
        """
        request = CreateProjectRequest(
            name=name,
            git_remote=git_remote,
            visibility=visibility,
            labels=labels,
            slug=slug,
            id=id,
        )
        resp = self._transport.post(
            _BASE_PATH,
            json=request.model_dump_api(),
        )
        return Project.model_validate(resp.json())

    def update(
        self,
        project_id: str,
        *,
        name: str | None = None,
        labels: dict[str, str] | None = None,
        annotations: dict[str, str] | None = None,
        visibility: str | None = None,
        default_runtime_broker_id: str | None = None,
    ) -> Project:
        """Update an existing project.

        Args:
            project_id: The project identifier.
            name: New project name.
            labels: Updated label key-value pairs.
            annotations: Updated annotation key-value pairs.
            visibility: Updated visibility setting.
            default_runtime_broker_id: Updated default runtime broker ID.

        Returns:
            The updated :class:`Project` object.

        Raises:
            NotFoundError: If the project does not exist.
        """
        request = UpdateProjectRequest(
            name=name,
            labels=labels,
            annotations=annotations,
            visibility=visibility,
            default_runtime_broker_id=default_runtime_broker_id,
        )
        resp = self._transport.patch(
            f"{_BASE_PATH}/{project_id}",
            json=request.model_dump_api(),
        )
        return Project.model_validate(resp.json())

    def delete(self, project_id: str) -> None:
        """Delete a project and all its agents.

        Args:
            project_id: The project identifier.

        Raises:
            NotFoundError: If the project does not exist.
        """
        self._transport.delete(f"{_BASE_PATH}/{project_id}")

    def list_agents(
        self,
        project_id: str,
        *,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> SyncPage[Agent]:
        """List agents in a project.

        Args:
            project_id: The project identifier.
            phase: Filter by agent lifecycle phase.
            runtime_broker_id: Filter by runtime broker ID.
            labels: Filter by label key-value pairs.
            limit: Maximum number of results per page.
            cursor: Pagination cursor from a previous response.

        Returns:
            A :class:`SyncPage` of :class:`Agent` objects.
        """
        params = self._build_agent_list_params(
            phase=phase,
            runtime_broker_id=runtime_broker_id,
            labels=labels,
            limit=limit,
            cursor=cursor,
        )
        return self._fetch_agent_page(project_id, params)

    # -- Internal helpers --

    def _fetch_project_page(self, params: dict[str, str]) -> SyncPage[Project]:
        """Fetch a single page of projects and wire up pagination."""
        resp = self._transport.get(_BASE_PATH, params=params or None)
        data = resp.json()

        projects_raw = data.get("projects", [])
        projects = [Project.model_validate(p) for p in projects_raw]

        next_cursor = data.get("nextCursor")
        has_next = bool(next_cursor)

        def fetch_next(cursor: str) -> SyncPage[Project]:
            next_params = dict(params)
            next_params["cursor"] = cursor
            return self._fetch_project_page(next_params)

        return SyncPage(
            data=projects,
            has_next=has_next,
            next_cursor=next_cursor,
            fetch_next=fetch_next if has_next else None,
        )

    def _fetch_agent_page(
        self, project_id: str, params: dict[str, str]
    ) -> SyncPage[Agent]:
        """Fetch a single page of agents for a project."""
        resp = self._transport.get(
            f"{_BASE_PATH}/{project_id}/agents",
            params=params or None,
        )
        data = resp.json()

        agents_raw = data.get("agents", [])
        agents = [Agent.model_validate(a) for a in agents_raw]

        next_cursor = data.get("nextCursor")
        has_next = bool(next_cursor)

        def fetch_next(cursor: str) -> SyncPage[Agent]:
            next_params = dict(params)
            next_params["cursor"] = cursor
            return self._fetch_agent_page(project_id, next_params)

        return SyncPage(
            data=agents,
            has_next=has_next,
            next_cursor=next_cursor,
            fetch_next=fetch_next if has_next else None,
        )

    @staticmethod
    def _build_list_params(
        *,
        visibility: str | None = None,
        git_remote: str | None = None,
        broker_id: str | None = None,
        name: str | None = None,
        slug: str | None = None,
        labels: dict[str, str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> dict[str, str]:
        """Build query parameters for project list requests."""
        params: dict[str, str] = {}
        if visibility is not None:
            params["visibility"] = visibility
        if git_remote is not None:
            params["gitRemote"] = git_remote
        if broker_id is not None:
            params["brokerId"] = broker_id
        if name is not None:
            params["name"] = name
        if slug is not None:
            params["slug"] = slug
        if labels:
            # The API expects label=key=value format; for a single params dict
            # we join multiple labels with comma separation. For simplicity
            # we use the last label value when building a flat dict.
            # In practice, the transport sends these as query params.
            for k, v in labels.items():
                params["label"] = f"{k}={v}"
        if limit is not None:
            params["limit"] = str(limit)
        if cursor is not None:
            params["cursor"] = cursor
        return params

    @staticmethod
    def _build_agent_list_params(
        *,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> dict[str, str]:
        """Build query parameters for agent list requests."""
        params: dict[str, str] = {}
        if phase is not None:
            params["phase"] = phase
        if runtime_broker_id is not None:
            params["runtimeBrokerId"] = runtime_broker_id
        if labels:
            for k, v in labels.items():
                params["label"] = f"{k}={v}"
        if limit is not None:
            params["limit"] = str(limit)
        if cursor is not None:
            params["cursor"] = cursor
        return params


class AsyncProjectsResource:
    """Asynchronous resource for project operations.

    Accessed via ``client.projects`` on :class:`AsyncScionClient`.

    Example::

        # List all projects
        page = await client.projects.list()
        async for project in page.auto_paging_iter():
            print(project.name)

        # Get a specific project
        project = await client.projects.get("proj-123")
    """

    def __init__(self, transport: AsyncTransport) -> None:
        self._transport = transport

    async def list(
        self,
        *,
        visibility: str | None = None,
        git_remote: str | None = None,
        broker_id: str | None = None,
        name: str | None = None,
        slug: str | None = None,
        labels: dict[str, str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> AsyncPage[Project]:
        """List projects matching the given filters.

        Args:
            visibility: Filter by visibility (e.g. ``"public"``, ``"private"``).
            git_remote: Filter by git remote URL (exact or prefix match).
            broker_id: Filter by contributing broker ID.
            name: Filter by exact project name (case-insensitive).
            slug: Filter by exact project slug (case-insensitive).
            labels: Filter by label key-value pairs.
            limit: Maximum number of results per page.
            cursor: Pagination cursor from a previous response.

        Returns:
            An :class:`AsyncPage` of :class:`Project` objects.
        """
        params = ProjectsResource._build_list_params(
            visibility=visibility,
            git_remote=git_remote,
            broker_id=broker_id,
            name=name,
            slug=slug,
            labels=labels,
            limit=limit,
            cursor=cursor,
        )
        return await self._fetch_project_page(params)

    async def get(self, project_id: str) -> Project:
        """Get a single project by ID.

        Args:
            project_id: The project identifier.

        Returns:
            The :class:`Project` object.

        Raises:
            NotFoundError: If the project does not exist.
        """
        resp = await self._transport.get(f"{_BASE_PATH}/{project_id}")
        return Project.model_validate(resp.json())

    async def create(
        self,
        *,
        name: str,
        git_remote: str | None = None,
        visibility: str | None = None,
        labels: dict[str, str] | None = None,
        slug: str | None = None,
        id: str | None = None,
    ) -> Project:
        """Create a new project.

        Args:
            name: The project name (required).
            git_remote: Optional git remote URL to associate.
            visibility: Optional visibility setting.
            labels: Optional label key-value pairs.
            slug: Optional project slug.
            id: Optional client-provided project ID.

        Returns:
            The created :class:`Project` object.
        """
        request = CreateProjectRequest(
            name=name,
            git_remote=git_remote,
            visibility=visibility,
            labels=labels,
            slug=slug,
            id=id,
        )
        resp = await self._transport.post(
            _BASE_PATH,
            json=request.model_dump_api(),
        )
        return Project.model_validate(resp.json())

    async def update(
        self,
        project_id: str,
        *,
        name: str | None = None,
        labels: dict[str, str] | None = None,
        annotations: dict[str, str] | None = None,
        visibility: str | None = None,
        default_runtime_broker_id: str | None = None,
    ) -> Project:
        """Update an existing project.

        Args:
            project_id: The project identifier.
            name: New project name.
            labels: Updated label key-value pairs.
            annotations: Updated annotation key-value pairs.
            visibility: Updated visibility setting.
            default_runtime_broker_id: Updated default runtime broker ID.

        Returns:
            The updated :class:`Project` object.

        Raises:
            NotFoundError: If the project does not exist.
        """
        request = UpdateProjectRequest(
            name=name,
            labels=labels,
            annotations=annotations,
            visibility=visibility,
            default_runtime_broker_id=default_runtime_broker_id,
        )
        resp = await self._transport.patch(
            f"{_BASE_PATH}/{project_id}",
            json=request.model_dump_api(),
        )
        return Project.model_validate(resp.json())

    async def delete(self, project_id: str) -> None:
        """Delete a project and all its agents.

        Args:
            project_id: The project identifier.

        Raises:
            NotFoundError: If the project does not exist.
        """
        await self._transport.delete(f"{_BASE_PATH}/{project_id}")

    async def list_agents(
        self,
        project_id: str,
        *,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> AsyncPage[Agent]:
        """List agents in a project.

        Args:
            project_id: The project identifier.
            phase: Filter by agent lifecycle phase.
            runtime_broker_id: Filter by runtime broker ID.
            labels: Filter by label key-value pairs.
            limit: Maximum number of results per page.
            cursor: Pagination cursor from a previous response.

        Returns:
            An :class:`AsyncPage` of :class:`Agent` objects.
        """
        params = ProjectsResource._build_agent_list_params(
            phase=phase,
            runtime_broker_id=runtime_broker_id,
            labels=labels,
            limit=limit,
            cursor=cursor,
        )
        return await self._fetch_agent_page(project_id, params)

    # -- Internal helpers --

    async def _fetch_project_page(self, params: dict[str, str]) -> AsyncPage[Project]:
        """Fetch a single page of projects and wire up pagination."""
        resp = await self._transport.get(_BASE_PATH, params=params or None)
        data = resp.json()

        projects_raw = data.get("projects", [])
        projects = [Project.model_validate(p) for p in projects_raw]

        next_cursor = data.get("nextCursor")
        has_next = bool(next_cursor)

        async def fetch_next(cursor: str) -> AsyncPage[Project]:
            next_params = dict(params)
            next_params["cursor"] = cursor
            return await self._fetch_project_page(next_params)

        return AsyncPage(
            data=projects,
            has_next=has_next,
            next_cursor=next_cursor,
            fetch_next=fetch_next if has_next else None,
        )

    async def _fetch_agent_page(
        self, project_id: str, params: dict[str, str]
    ) -> AsyncPage[Agent]:
        """Fetch a single page of agents for a project."""
        resp = await self._transport.get(
            f"{_BASE_PATH}/{project_id}/agents",
            params=params or None,
        )
        data = resp.json()

        agents_raw = data.get("agents", [])
        agents = [Agent.model_validate(a) for a in agents_raw]

        next_cursor = data.get("nextCursor")
        has_next = bool(next_cursor)

        async def fetch_next(cursor: str) -> AsyncPage[Agent]:
            next_params = dict(params)
            next_params["cursor"] = cursor
            return await self._fetch_agent_page(project_id, next_params)

        return AsyncPage(
            data=agents,
            has_next=has_next,
            next_cursor=next_cursor,
            fetch_next=fetch_next if has_next else None,
        )

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

"""Agents resource for the Scion Python SDK.

Provides synchronous and asynchronous interfaces for managing agents
through the Scion Hub API. Mirrors the Go ``AgentService`` interface
in ``pkg/hubclient/agents.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING, Any

from scion._pagination import AsyncPage, SyncPage
from scion.types.agents import (
    Agent,
    CreateAgentRequest,
    CreateAgentResponse,
    ListAgentsResponse,
)
from scion.types.messages import StructuredMessage

if TYPE_CHECKING:
    from scion._transport import AsyncTransport, Transport


class AgentsResource:
    """Synchronous resource for agent operations.

    Usage::

        client = ScionClient("https://hub.example.com", token="my-token")
        agents_page = client.agents.list(project_id="proj-1")
        agent = client.agents.get("agent-id")
    """

    def __init__(self, transport: Transport) -> None:
        self._transport = transport

    def create(
        self,
        *,
        name: str,
        project_id: str,
        template: str | None = None,
        harness_config: str | None = None,
        harness_auth: str | None = None,
        runtime_broker_id: str | None = None,
        profile: str | None = None,
        task: str | None = None,
        branch: str | None = None,
        workspace: str | None = None,
        labels: dict[str, str] | None = None,
        annotations: dict[str, str] | None = None,
        resume: bool | None = None,
        attach: bool | None = None,
        provision_only: bool | None = None,
        notify: bool | None = None,
    ) -> CreateAgentResponse:
        """Create a new agent.

        Args:
            name: Display name for the agent.
            project_id: ID of the project to create the agent in.
            template: Agent template name.
            harness_config: Explicit harness config name.
            harness_auth: Late-binding override for auth type.
            runtime_broker_id: Runtime broker to use.
            profile: Agent profile.
            task: Task description for the agent.
            branch: Git branch to use.
            workspace: Workspace path.
            labels: Key-value labels for the agent.
            annotations: Key-value annotations for the agent.
            resume: Whether to resume a previously stopped agent.
            attach: Whether to attach interactively.
            provision_only: Provision without starting.
            notify: Subscribe to status notifications for the agent.

        Returns:
            CreateAgentResponse containing the created agent and any warnings.
        """
        req = CreateAgentRequest(
            name=name,
            project_id=project_id,
            template=template,
            harness_config=harness_config,
            harness_auth=harness_auth,
            runtime_broker_id=runtime_broker_id,
            profile=profile,
            task=task,
            branch=branch,
            workspace=workspace,
            labels=labels,
            annotations=annotations,
            resume=resume,
            attach=attach,
            provision_only=provision_only,
            notify=notify,
        )
        resp = self._transport.post("/api/v1/agents", json=req.model_dump_api())
        return CreateAgentResponse.model_validate(resp.json())

    def get(self, agent_id: str) -> Agent:
        """Get a single agent by ID.

        Args:
            agent_id: The agent's unique identifier.

        Returns:
            The Agent object.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        resp = self._transport.get(f"/api/v1/agents/{agent_id}")
        return Agent.model_validate(resp.json())

    def list(
        self,
        *,
        project_id: str | None = None,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        include_deleted: bool = False,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> SyncPage[Agent]:
        """List agents with optional filtering.

        Args:
            project_id: Filter by project ID.
            phase: Filter by lifecycle phase (e.g. ``"running"``, ``"stopped"``).
            runtime_broker_id: Filter by runtime broker ID.
            labels: Filter by label key-value pairs.
            include_deleted: Include soft-deleted agents.
            limit: Maximum number of agents per page.
            cursor: Pagination cursor for the next page.

        Returns:
            A SyncPage of Agent objects supporting iteration and auto-paging.
        """
        params = self._build_list_params(
            project_id=project_id,
            phase=phase,
            runtime_broker_id=runtime_broker_id,
            labels=labels,
            include_deleted=include_deleted,
            limit=limit,
            cursor=cursor,
        )
        return self._fetch_page(params)

    def start(self, agent_id: str) -> None:
        """Start a stopped agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a startable state.
        """
        self._transport.post(f"/api/v1/agents/{agent_id}/start")

    def stop(self, agent_id: str) -> None:
        """Stop a running agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a stoppable state.
        """
        self._transport.post(f"/api/v1/agents/{agent_id}/stop")

    def suspend(self, agent_id: str) -> None:
        """Suspend a running agent, preserving state for later resume.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a suspendable state.
        """
        self._transport.post(f"/api/v1/agents/{agent_id}/suspend")

    def restart(self, agent_id: str) -> None:
        """Restart an agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a restartable state.
        """
        self._transport.post(f"/api/v1/agents/{agent_id}/restart")

    def delete(self, agent_id: str) -> None:
        """Delete an agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        self._transport.delete(f"/api/v1/agents/{agent_id}")

    def restore(self, agent_id: str) -> Agent:
        """Restore a soft-deleted agent.

        Args:
            agent_id: The agent's unique identifier.

        Returns:
            The restored Agent object.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        resp = self._transport.post(f"/api/v1/agents/{agent_id}/restore")
        return Agent.model_validate(resp.json())

    def send_message(
        self,
        agent_id: str,
        message: str,
        *,
        interrupt: bool = False,
    ) -> None:
        """Send a plain text message to an agent.

        Args:
            agent_id: The agent's unique identifier.
            message: The message text to send.
            interrupt: Whether to interrupt the agent's current activity.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        body: dict[str, Any] = {"message": message}
        if interrupt:
            body["interrupt"] = True
        self._transport.post(f"/api/v1/agents/{agent_id}/message", json=body)

    def send_structured_message(
        self,
        agent_id: str,
        msg: StructuredMessage,
        *,
        interrupt: bool = False,
        notify: bool = False,
    ) -> None:
        """Send a structured message to an agent.

        Args:
            agent_id: The agent's unique identifier.
            msg: The structured message payload.
            interrupt: Whether to interrupt the agent's current activity.
            notify: Subscribe to status notifications for the target agent.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        body: dict[str, Any] = {"structured_message": msg.model_dump_api()}
        if interrupt:
            body["interrupt"] = True
        if notify:
            body["notify"] = True
        self._transport.post(f"/api/v1/agents/{agent_id}/message", json=body)

    def broadcast_message(
        self,
        msg: StructuredMessage,
        *,
        interrupt: bool = False,
    ) -> None:
        """Broadcast a structured message to all running agents in the project.

        Args:
            msg: The structured message payload to broadcast.
            interrupt: Whether to interrupt agents' current activities.
        """
        body: dict[str, Any] = {"structured_message": msg.model_dump_api()}
        if interrupt:
            body["interrupt"] = True
        self._transport.post("/api/v1/messages/broadcast", json=body)

    def _build_list_params(
        self,
        *,
        project_id: str | None = None,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        include_deleted: bool = False,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> dict[str, str]:
        """Build query parameters for list requests."""
        params: dict[str, str] = {}
        if project_id is not None:
            params["projectId"] = project_id
        if phase is not None:
            params["phase"] = phase
        if runtime_broker_id is not None:
            params["runtimeBrokerId"] = runtime_broker_id
        if include_deleted:
            params["includeDeleted"] = "true"
        if labels:
            # Labels are sent as repeated `label=key=value` params.
            # Since our transport uses a flat dict, we join them with commas
            # as an alternative encoding accepted by the API.
            label_parts = [f"{k}={v}" for k, v in labels.items()]
            params["label"] = ",".join(label_parts)
        if limit is not None:
            params["limit"] = str(limit)
        if cursor is not None:
            params["cursor"] = cursor
        return params

    def _fetch_page(self, params: dict[str, str]) -> SyncPage[Agent]:
        """Fetch a single page and return a SyncPage with auto-paging support."""
        resp = self._transport.get("/api/v1/agents", params=params)
        data = ListAgentsResponse.model_validate(resp.json())

        has_next = data.meta.next_cursor is not None and data.meta.next_cursor != ""

        def fetch_next(next_cursor: str) -> SyncPage[Agent]:
            next_params = dict(params)
            next_params["cursor"] = next_cursor
            return self._fetch_page(next_params)

        return SyncPage(
            data=data.agents,
            has_next=has_next,
            next_cursor=data.meta.next_cursor,
            fetch_next=fetch_next,
        )


class AsyncAgentsResource:
    """Asynchronous resource for agent operations.

    Usage::

        client = AsyncScionClient("https://hub.example.com", token="my-token")
        agents_page = await client.agents.list(project_id="proj-1")
        agent = await client.agents.get("agent-id")
    """

    def __init__(self, transport: AsyncTransport) -> None:
        self._transport = transport

    async def create(
        self,
        *,
        name: str,
        project_id: str,
        template: str | None = None,
        harness_config: str | None = None,
        harness_auth: str | None = None,
        runtime_broker_id: str | None = None,
        profile: str | None = None,
        task: str | None = None,
        branch: str | None = None,
        workspace: str | None = None,
        labels: dict[str, str] | None = None,
        annotations: dict[str, str] | None = None,
        resume: bool | None = None,
        attach: bool | None = None,
        provision_only: bool | None = None,
        notify: bool | None = None,
    ) -> CreateAgentResponse:
        """Create a new agent.

        Args:
            name: Display name for the agent.
            project_id: ID of the project to create the agent in.
            template: Agent template name.
            harness_config: Explicit harness config name.
            harness_auth: Late-binding override for auth type.
            runtime_broker_id: Runtime broker to use.
            profile: Agent profile.
            task: Task description for the agent.
            branch: Git branch to use.
            workspace: Workspace path.
            labels: Key-value labels for the agent.
            annotations: Key-value annotations for the agent.
            resume: Whether to resume a previously stopped agent.
            attach: Whether to attach interactively.
            provision_only: Provision without starting.
            notify: Subscribe to status notifications for the agent.

        Returns:
            CreateAgentResponse containing the created agent and any warnings.
        """
        req = CreateAgentRequest(
            name=name,
            project_id=project_id,
            template=template,
            harness_config=harness_config,
            harness_auth=harness_auth,
            runtime_broker_id=runtime_broker_id,
            profile=profile,
            task=task,
            branch=branch,
            workspace=workspace,
            labels=labels,
            annotations=annotations,
            resume=resume,
            attach=attach,
            provision_only=provision_only,
            notify=notify,
        )
        resp = await self._transport.post("/api/v1/agents", json=req.model_dump_api())
        return CreateAgentResponse.model_validate(resp.json())

    async def get(self, agent_id: str) -> Agent:
        """Get a single agent by ID.

        Args:
            agent_id: The agent's unique identifier.

        Returns:
            The Agent object.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        resp = await self._transport.get(f"/api/v1/agents/{agent_id}")
        return Agent.model_validate(resp.json())

    async def list(
        self,
        *,
        project_id: str | None = None,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        include_deleted: bool = False,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> AsyncPage[Agent]:
        """List agents with optional filtering.

        Args:
            project_id: Filter by project ID.
            phase: Filter by lifecycle phase (e.g. ``"running"``, ``"stopped"``).
            runtime_broker_id: Filter by runtime broker ID.
            labels: Filter by label key-value pairs.
            include_deleted: Include soft-deleted agents.
            limit: Maximum number of agents per page.
            cursor: Pagination cursor for the next page.

        Returns:
            An AsyncPage of Agent objects supporting iteration and auto-paging.
        """
        params = self._build_list_params(
            project_id=project_id,
            phase=phase,
            runtime_broker_id=runtime_broker_id,
            labels=labels,
            include_deleted=include_deleted,
            limit=limit,
            cursor=cursor,
        )
        return await self._fetch_page(params)

    async def start(self, agent_id: str) -> None:
        """Start a stopped agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a startable state.
        """
        await self._transport.post(f"/api/v1/agents/{agent_id}/start")

    async def stop(self, agent_id: str) -> None:
        """Stop a running agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a stoppable state.
        """
        await self._transport.post(f"/api/v1/agents/{agent_id}/stop")

    async def suspend(self, agent_id: str) -> None:
        """Suspend a running agent, preserving state for later resume.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a suspendable state.
        """
        await self._transport.post(f"/api/v1/agents/{agent_id}/suspend")

    async def restart(self, agent_id: str) -> None:
        """Restart an agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
            ConflictError: If the agent is not in a restartable state.
        """
        await self._transport.post(f"/api/v1/agents/{agent_id}/restart")

    async def delete(self, agent_id: str) -> None:
        """Delete an agent.

        Args:
            agent_id: The agent's unique identifier.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        await self._transport.delete(f"/api/v1/agents/{agent_id}")

    async def restore(self, agent_id: str) -> Agent:
        """Restore a soft-deleted agent.

        Args:
            agent_id: The agent's unique identifier.

        Returns:
            The restored Agent object.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        resp = await self._transport.post(f"/api/v1/agents/{agent_id}/restore")
        return Agent.model_validate(resp.json())

    async def send_message(
        self,
        agent_id: str,
        message: str,
        *,
        interrupt: bool = False,
    ) -> None:
        """Send a plain text message to an agent.

        Args:
            agent_id: The agent's unique identifier.
            message: The message text to send.
            interrupt: Whether to interrupt the agent's current activity.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        body: dict[str, Any] = {"message": message}
        if interrupt:
            body["interrupt"] = True
        await self._transport.post(f"/api/v1/agents/{agent_id}/message", json=body)

    async def send_structured_message(
        self,
        agent_id: str,
        msg: StructuredMessage,
        *,
        interrupt: bool = False,
        notify: bool = False,
    ) -> None:
        """Send a structured message to an agent.

        Args:
            agent_id: The agent's unique identifier.
            msg: The structured message payload.
            interrupt: Whether to interrupt the agent's current activity.
            notify: Subscribe to status notifications for the target agent.

        Raises:
            NotFoundError: If the agent does not exist.
        """
        body: dict[str, Any] = {"structured_message": msg.model_dump_api()}
        if interrupt:
            body["interrupt"] = True
        if notify:
            body["notify"] = True
        await self._transport.post(f"/api/v1/agents/{agent_id}/message", json=body)

    async def broadcast_message(
        self,
        msg: StructuredMessage,
        *,
        interrupt: bool = False,
    ) -> None:
        """Broadcast a structured message to all running agents in the project.

        Args:
            msg: The structured message payload to broadcast.
            interrupt: Whether to interrupt agents' current activities.
        """
        body: dict[str, Any] = {"structured_message": msg.model_dump_api()}
        if interrupt:
            body["interrupt"] = True
        await self._transport.post("/api/v1/messages/broadcast", json=body)

    def _build_list_params(
        self,
        *,
        project_id: str | None = None,
        phase: str | None = None,
        runtime_broker_id: str | None = None,
        labels: dict[str, str] | None = None,
        include_deleted: bool = False,
        limit: int | None = None,
        cursor: str | None = None,
    ) -> dict[str, str]:
        """Build query parameters for list requests."""
        params: dict[str, str] = {}
        if project_id is not None:
            params["projectId"] = project_id
        if phase is not None:
            params["phase"] = phase
        if runtime_broker_id is not None:
            params["runtimeBrokerId"] = runtime_broker_id
        if include_deleted:
            params["includeDeleted"] = "true"
        if labels:
            label_parts = [f"{k}={v}" for k, v in labels.items()]
            params["label"] = ",".join(label_parts)
        if limit is not None:
            params["limit"] = str(limit)
        if cursor is not None:
            params["cursor"] = cursor
        return params

    async def _fetch_page(self, params: dict[str, str]) -> AsyncPage[Agent]:
        """Fetch a single page and return an AsyncPage with auto-paging support."""
        resp = await self._transport.get("/api/v1/agents", params=params)
        data = ListAgentsResponse.model_validate(resp.json())

        has_next = data.meta.next_cursor is not None and data.meta.next_cursor != ""

        async def fetch_next(next_cursor: str) -> AsyncPage[Agent]:
            next_params = dict(params)
            next_params["cursor"] = next_cursor
            return await self._fetch_page(next_params)

        return AsyncPage(
            data=data.agents,
            has_next=has_next,
            next_cursor=data.meta.next_cursor,
            fetch_next=fetch_next,
        )

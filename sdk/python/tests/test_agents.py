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

"""Tests for the AgentsResource and AsyncAgentsResource."""

from __future__ import annotations

import json

import httpx
import pytest
import respx

from scion import (
    AsyncScionClient,
    ConflictError,
    NotFoundError,
    ScionClient,
    ValidationError,
)
from scion.types.agents import Agent
from scion.types.messages import StructuredMessage

BASE_URL = "https://hub.example.com"

# --- Fixtures for reusable mock data ---

AGENT_JSON = {
    "id": "agent-123",
    "slug": "my-agent",
    "containerId": "ctr-abc",
    "name": "Test Agent",
    "projectId": "proj-1",
    "phase": "running",
    "status": "active",
    "connectionState": "connected",
    "runtimeBrokerId": "broker-1",
    "stateVersion": 5,
}

AGENT_JSON_2 = {
    "id": "agent-456",
    "slug": "other-agent",
    "containerId": "ctr-def",
    "name": "Other Agent",
    "projectId": "proj-1",
    "phase": "stopped",
    "status": "inactive",
    "stateVersion": 2,
}

CREATE_RESPONSE_JSON = {
    "agent": AGENT_JSON,
    "warnings": ["some warning"],
}


def _error_body(code: str, message: str) -> bytes:
    """Build a JSON error response body."""
    return json.dumps({"error": {"code": code, "message": message}}).encode()


class TestAgentsResource:
    """Tests for the synchronous AgentsResource."""

    def _make_client(self) -> ScionClient:
        return ScionClient(BASE_URL, token="test-token")

    # -- create --

    def test_create(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(200, json=CREATE_RESPONSE_JSON)
            )
            client = self._make_client()
            result = client.agents.create(name="Test Agent", project_id="proj-1")

            assert result.agent is not None
            assert result.agent.id == "agent-123"
            assert result.agent.name == "Test Agent"
            assert result.warnings == ["some warning"]

            # Verify the request body
            req_body = json.loads(route.calls[0].request.content)
            assert req_body["name"] == "Test Agent"
            assert req_body["projectId"] == "proj-1"
            client.close()

    def test_create_with_optional_fields(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(200, json=CREATE_RESPONSE_JSON)
            )
            client = self._make_client()
            client.agents.create(
                name="Test Agent",
                project_id="proj-1",
                template="my-template",
                task="do something",
                labels={"env": "test"},
                provision_only=True,
            )

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["template"] == "my-template"
            assert req_body["task"] == "do something"
            assert req_body["labels"] == {"env": "test"}
            assert req_body["provisionOnly"] is True
            # None fields should be excluded
            assert "branch" not in req_body
            assert "workspace" not in req_body
            client.close()

    def test_create_validation_error(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(
                    400,
                    content=_error_body("validation_error", "name is required"),
                )
            )
            client = self._make_client()
            with pytest.raises(ValidationError, match="name is required"):
                client.agents.create(name="", project_id="proj-1")
            client.close()

    # -- get --

    def test_get(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents/agent-123").mock(
                return_value=httpx.Response(200, json=AGENT_JSON)
            )
            client = self._make_client()
            agent = client.agents.get("agent-123")

            assert agent.id == "agent-123"
            assert agent.name == "Test Agent"
            assert agent.project_id == "proj-1"
            assert agent.phase == "running"
            assert agent.state_version == 5
            client.close()

    def test_get_not_found(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents/nonexistent").mock(
                return_value=httpx.Response(
                    404,
                    content=_error_body("not_found", "agent not found"),
                )
            )
            client = self._make_client()
            with pytest.raises(NotFoundError, match="agent not found"):
                client.agents.get("nonexistent")
            client.close()

    # -- list --

    def test_list_basic(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "agents": [AGENT_JSON, AGENT_JSON_2],
                        "meta": {"next_cursor": None, "total_count": 2},
                    },
                )
            )
            client = self._make_client()
            page = client.agents.list()

            assert len(page) == 2
            assert page.data[0].id == "agent-123"
            assert page.data[1].id == "agent-456"
            assert not page.has_next
            client.close()

    def test_list_with_filters(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={"agents": [AGENT_JSON], "meta": {}},
                )
            )
            client = self._make_client()
            client.agents.list(
                project_id="proj-1",
                phase="running",
                runtime_broker_id="broker-1",
                include_deleted=True,
                limit=10,
            )

            request = route.calls[0].request
            assert request.url.params["projectId"] == "proj-1"
            assert request.url.params["phase"] == "running"
            assert request.url.params["runtimeBrokerId"] == "broker-1"
            assert request.url.params["includeDeleted"] == "true"
            assert request.url.params["limit"] == "10"
            client.close()

    def test_list_pagination(self) -> None:
        """Test that list supports multi-page iteration."""
        with respx.mock:
            # Page 1
            respx.get(f"{BASE_URL}/api/v1/agents").mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "agents": [AGENT_JSON],
                            "meta": {"next_cursor": "cursor-2", "total_count": 2},
                        },
                    ),
                    # Page 2 (fetched via auto-paging)
                    httpx.Response(
                        200,
                        json={
                            "agents": [AGENT_JSON_2],
                            "meta": {"next_cursor": None, "total_count": 2},
                        },
                    ),
                ]
            )
            client = self._make_client()
            page = client.agents.list(limit=1)

            assert page.has_next
            assert page.next_cursor == "cursor-2"

            # Auto-paging iteration
            all_agents = list(page.auto_paging_iter())
            assert len(all_agents) == 2
            assert all_agents[0].id == "agent-123"
            assert all_agents[1].id == "agent-456"
            client.close()

    def test_list_empty(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={"agents": [], "meta": {}},
                )
            )
            client = self._make_client()
            page = client.agents.list()

            assert len(page) == 0
            assert not page.has_next
            client.close()

    # -- start / stop / suspend / restart --

    def test_start(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/start").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.start("agent-123")

            assert route.called
            client.close()

    def test_stop(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/stop").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.stop("agent-123")

            assert route.called
            client.close()

    def test_suspend(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/suspend").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.suspend("agent-123")

            assert route.called
            client.close()

    def test_restart(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/restart").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.restart("agent-123")

            assert route.called
            client.close()

    def test_start_conflict(self) -> None:
        """Starting an already-running agent should raise ConflictError."""
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents/agent-123/start").mock(
                return_value=httpx.Response(
                    409,
                    content=_error_body("conflict", "agent is already running"),
                )
            )
            client = self._make_client()
            with pytest.raises(ConflictError, match="already running"):
                client.agents.start("agent-123")
            client.close()

    def test_stop_not_found(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents/bad-id/stop").mock(
                return_value=httpx.Response(
                    404,
                    content=_error_body("not_found", "agent not found"),
                )
            )
            client = self._make_client()
            with pytest.raises(NotFoundError):
                client.agents.stop("bad-id")
            client.close()

    # -- delete --

    def test_delete(self) -> None:
        with respx.mock:
            route = respx.delete(f"{BASE_URL}/api/v1/agents/agent-123").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.delete("agent-123")

            assert route.called
            client.close()

    def test_delete_not_found(self) -> None:
        with respx.mock:
            respx.delete(f"{BASE_URL}/api/v1/agents/bad-id").mock(
                return_value=httpx.Response(
                    404,
                    content=_error_body("not_found", "agent not found"),
                )
            )
            client = self._make_client()
            with pytest.raises(NotFoundError):
                client.agents.delete("bad-id")
            client.close()

    # -- restore --

    def test_restore(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents/agent-123/restore").mock(
                return_value=httpx.Response(200, json=AGENT_JSON)
            )
            client = self._make_client()
            agent = client.agents.restore("agent-123")

            assert agent.id == "agent-123"
            assert isinstance(agent, Agent)
            client.close()

    # -- send_message --

    def test_send_message(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/message").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.send_message("agent-123", "hello")

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["message"] == "hello"
            assert "interrupt" not in req_body
            client.close()

    def test_send_message_with_interrupt(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/message").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            client.agents.send_message("agent-123", "urgent!", interrupt=True)

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["message"] == "urgent!"
            assert req_body["interrupt"] is True
            client.close()

    # -- send_structured_message --

    def test_send_structured_message(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/message").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            msg = StructuredMessage(
                type="task",
                content="do something",
                data={"key": "value"},
            )
            client.agents.send_structured_message("agent-123", msg)

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["structured_message"]["type"] == "task"
            assert req_body["structured_message"]["content"] == "do something"
            assert req_body["structured_message"]["data"] == {"key": "value"}
            assert "interrupt" not in req_body
            assert "notify" not in req_body
            client.close()

    def test_send_structured_message_with_flags(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/message").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            msg = StructuredMessage(type="command", content="run tests")
            client.agents.send_structured_message(
                "agent-123", msg, interrupt=True, notify=True
            )

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["interrupt"] is True
            assert req_body["notify"] is True
            client.close()

    # -- broadcast_message --

    def test_broadcast_message(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/messages/broadcast").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            msg = StructuredMessage(type="announcement", content="hello all")
            client.agents.broadcast_message(msg)

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["structured_message"]["type"] == "announcement"
            assert req_body["structured_message"]["content"] == "hello all"
            assert "interrupt" not in req_body
            client.close()

    def test_broadcast_message_with_interrupt(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/messages/broadcast").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            msg = StructuredMessage(type="urgent", content="stop everything")
            client.agents.broadcast_message(msg, interrupt=True)

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["interrupt"] is True
            client.close()

    # -- client property --

    def test_agents_property_returns_same_instance(self) -> None:
        """The agents property should return the same instance on repeated access."""
        client = self._make_client()
        agents1 = client.agents
        agents2 = client.agents
        assert agents1 is agents2
        client.close()


class TestAsyncAgentsResource:
    """Tests for the asynchronous AsyncAgentsResource."""

    def _make_client(self) -> AsyncScionClient:
        return AsyncScionClient(BASE_URL, token="test-token")

    @pytest.mark.asyncio
    async def test_create(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(200, json=CREATE_RESPONSE_JSON)
            )
            client = self._make_client()
            result = await client.agents.create(name="Test Agent", project_id="proj-1")

            assert result.agent is not None
            assert result.agent.id == "agent-123"
            await client.close()

    @pytest.mark.asyncio
    async def test_get(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents/agent-123").mock(
                return_value=httpx.Response(200, json=AGENT_JSON)
            )
            client = self._make_client()
            agent = await client.agents.get("agent-123")

            assert agent.id == "agent-123"
            assert agent.name == "Test Agent"
            await client.close()

    @pytest.mark.asyncio
    async def test_get_not_found(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents/bad-id").mock(
                return_value=httpx.Response(
                    404,
                    content=_error_body("not_found", "agent not found"),
                )
            )
            client = self._make_client()
            with pytest.raises(NotFoundError):
                await client.agents.get("bad-id")
            await client.close()

    @pytest.mark.asyncio
    async def test_list(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "agents": [AGENT_JSON],
                        "meta": {"next_cursor": None},
                    },
                )
            )
            client = self._make_client()
            page = await client.agents.list(project_id="proj-1")

            assert len(page) == 1
            assert page.data[0].id == "agent-123"
            await client.close()

    @pytest.mark.asyncio
    async def test_list_pagination(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/agents").mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "agents": [AGENT_JSON],
                            "meta": {"next_cursor": "page2"},
                        },
                    ),
                    httpx.Response(
                        200,
                        json={
                            "agents": [AGENT_JSON_2],
                            "meta": {"next_cursor": None},
                        },
                    ),
                ]
            )
            client = self._make_client()
            page = await client.agents.list(limit=1)

            assert page.has_next

            all_agents = [a async for a in page.auto_paging_iter()]
            assert len(all_agents) == 2
            assert all_agents[0].id == "agent-123"
            assert all_agents[1].id == "agent-456"
            await client.close()

    @pytest.mark.asyncio
    async def test_start(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/start").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            await client.agents.start("agent-123")

            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_stop(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/stop").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            await client.agents.stop("agent-123")

            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_suspend(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/suspend").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            await client.agents.suspend("agent-123")

            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_restart(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/restart").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            await client.agents.restart("agent-123")

            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_delete(self) -> None:
        with respx.mock:
            route = respx.delete(f"{BASE_URL}/api/v1/agents/agent-123").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            await client.agents.delete("agent-123")

            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_restore(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents/agent-123/restore").mock(
                return_value=httpx.Response(200, json=AGENT_JSON)
            )
            client = self._make_client()
            agent = await client.agents.restore("agent-123")

            assert agent.id == "agent-123"
            await client.close()

    @pytest.mark.asyncio
    async def test_send_message(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/message").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            await client.agents.send_message("agent-123", "hello")

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["message"] == "hello"
            await client.close()

    @pytest.mark.asyncio
    async def test_send_structured_message(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/agents/agent-123/message").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            msg = StructuredMessage(type="task", content="work")
            await client.agents.send_structured_message(
                "agent-123", msg, interrupt=True, notify=True
            )

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["structured_message"]["type"] == "task"
            assert req_body["interrupt"] is True
            assert req_body["notify"] is True
            await client.close()

    @pytest.mark.asyncio
    async def test_broadcast_message(self) -> None:
        with respx.mock:
            route = respx.post(f"{BASE_URL}/api/v1/messages/broadcast").mock(
                return_value=httpx.Response(200, json={})
            )
            client = self._make_client()
            msg = StructuredMessage(type="info", content="hello all")
            await client.agents.broadcast_message(msg)

            req_body = json.loads(route.calls[0].request.content)
            assert req_body["structured_message"]["type"] == "info"
            await client.close()

    @pytest.mark.asyncio
    async def test_conflict_error(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents/agent-123/start").mock(
                return_value=httpx.Response(
                    409,
                    content=_error_body("conflict", "agent is already running"),
                )
            )
            client = self._make_client()
            with pytest.raises(ConflictError, match="already running"):
                await client.agents.start("agent-123")
            await client.close()

    @pytest.mark.asyncio
    async def test_agents_property_returns_same_instance(self) -> None:
        client = self._make_client()
        agents1 = client.agents
        agents2 = client.agents
        assert agents1 is agents2
        await client.close()

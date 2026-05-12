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

"""Tests for type models."""

from scion.types.agents import (
    Agent,
    CreateAgentRequest,
    CreateAgentResponse,
    ListAgentsResponse,
    UpdateAgentRequest,
)
from scion.types.common import HealthResponse, PaginationParams
from scion.types.messages import AgentMessage, Message, StructuredMessage
from scion.types.projects import (
    CreateProjectRequest,
    Project,
    ProjectProvider,
    UpdateProjectRequest,
)
from scion.types.secrets import (
    ListSecretResponse,
    Secret,
    SetSecretRequest,
    SetSecretResponse,
)


class TestHealthResponse:
    def test_parse(self) -> None:
        data = {"status": "ok", "version": "1.0", "scionVersion": "0.5", "uptime": "1h"}
        resp = HealthResponse.model_validate(data)
        assert resp.status == "ok"
        assert resp.version == "1.0"

    def test_parse_minimal(self) -> None:
        resp = HealthResponse.model_validate({"status": "ok"})
        assert resp.status == "ok"
        assert resp.checks is None


class TestPaginationParams:
    def test_to_query_empty(self) -> None:
        params = PaginationParams()
        assert params.to_query() == {}

    def test_to_query_full(self) -> None:
        params = PaginationParams(cursor="abc", limit=50)
        q = params.to_query()
        assert q["cursor"] == "abc"
        assert q["limit"] == "50"


class TestAgent:
    def test_parse_full(self) -> None:
        data = {
            "id": "agent-1",
            "slug": "my-agent",
            "containerId": "ctr-1",
            "name": "Test Agent",
            "projectId": "proj-1",
            "project": "my-project",
            "phase": "running",
            "activity": "thinking",
            "status": "running",
            "runtimeBrokerId": "broker-1",
            "created": "2026-01-01T00:00:00Z",
            "updated": "2026-01-02T00:00:00Z",
        }
        agent = Agent.model_validate(data)
        assert agent.id == "agent-1"
        assert agent.slug == "my-agent"
        assert agent.container_id == "ctr-1"
        assert agent.project_id == "proj-1"
        assert agent.phase == "running"
        assert agent.runtime_broker_id == "broker-1"

    def test_parse_minimal(self) -> None:
        agent = Agent.model_validate({"id": "a1", "name": "test", "status": "created"})
        assert agent.id == "a1"
        assert agent.project_id is None

    def test_extra_fields_ignored(self) -> None:
        """Ensure unknown fields from the API don't cause errors."""
        data = {"id": "a1", "name": "test", "status": "ok", "unknownField": "value"}
        agent = Agent.model_validate(data)
        assert agent.id == "a1"


class TestCreateAgentRequest:
    def test_model_dump_api(self) -> None:
        req = CreateAgentRequest(name="agent-1", project_id="proj-1", task="do stuff")
        dump = req.model_dump_api()
        assert dump["name"] == "agent-1"
        assert dump["projectId"] == "proj-1"
        assert dump["task"] == "do stuff"
        assert "template" not in dump  # None excluded

    def test_from_alias(self) -> None:
        req = CreateAgentRequest.model_validate({"name": "a", "projectId": "p"})
        assert req.project_id == "p"


class TestCreateAgentResponse:
    def test_parse(self) -> None:
        data = {
            "agent": {"id": "a1", "name": "test", "status": "created"},
            "warnings": ["low resources"],
        }
        resp = CreateAgentResponse.model_validate(data)
        assert resp.agent is not None
        assert resp.agent.id == "a1"
        assert resp.warnings == ["low resources"]


class TestUpdateAgentRequest:
    def test_model_dump_api(self) -> None:
        req = UpdateAgentRequest(name="new-name", state_version=5)
        dump = req.model_dump_api()
        assert dump["name"] == "new-name"
        assert dump["stateVersion"] == 5


class TestListAgentsResponse:
    def test_parse(self) -> None:
        data = {
            "agents": [{"id": "a1", "name": "test", "status": "running"}],
            "meta": {"next_cursor": "cur1", "total_count": 10},
        }
        resp = ListAgentsResponse.model_validate(data)
        assert len(resp.agents) == 1
        assert resp.meta.next_cursor == "cur1"


class TestProject:
    def test_parse(self) -> None:
        data = {
            "id": "proj-1",
            "name": "My Project",
            "slug": "my-project",
            "gitRemote": "https://github.com/test/repo",
            "agentCount": 5,
            "projectType": "linked",
        }
        project = Project.model_validate(data)
        assert project.id == "proj-1"
        assert project.git_remote == "https://github.com/test/repo"
        assert project.agent_count == 5
        assert project.project_type == "linked"

    def test_parse_minimal(self) -> None:
        project = Project.model_validate({"id": "p1", "name": "p", "slug": "p"})
        assert project.id == "p1"


class TestCreateProjectRequest:
    def test_model_dump_api(self) -> None:
        req = CreateProjectRequest(name="proj", git_remote="https://github.com/test/repo")
        dump = req.model_dump_api()
        assert dump["name"] == "proj"
        assert dump["gitRemote"] == "https://github.com/test/repo"


class TestUpdateProjectRequest:
    def test_model_dump_api(self) -> None:
        req = UpdateProjectRequest(name="new-name", visibility="private")
        dump = req.model_dump_api()
        assert dump["name"] == "new-name"
        assert dump["visibility"] == "private"


class TestProjectProvider:
    def test_parse(self) -> None:
        data = {"brokerId": "b1", "brokerName": "local", "status": "active"}
        provider = ProjectProvider.model_validate(data)
        assert provider.broker_id == "b1"
        assert provider.broker_name == "local"


class TestSecret:
    def test_parse(self) -> None:
        data = {
            "id": "s1",
            "key": "API_KEY",
            "type": "environment",
            "scope": "user",
            "scopeId": "u1",
            "version": 3,
            "allowProgeny": True,
        }
        secret = Secret.model_validate(data)
        assert secret.id == "s1"
        assert secret.key == "API_KEY"
        assert secret.secret_type == "environment"
        assert secret.scope == "user"
        assert secret.scope_id == "u1"
        assert secret.version == 3
        assert secret.allow_progeny is True


class TestSetSecretRequest:
    def test_model_dump_api(self) -> None:
        req = SetSecretRequest(
            value="secret-val",
            scope="project",
            scope_id="proj-1",
            description="My API key",
        )
        dump = req.model_dump_api()
        assert dump["value"] == "secret-val"
        assert dump["scope"] == "project"
        assert dump["scopeId"] == "proj-1"
        assert dump["description"] == "My API key"


class TestSetSecretResponse:
    def test_parse(self) -> None:
        data = {
            "secret": {"id": "s1", "key": "API_KEY", "scope": "user", "scopeId": "u1"},
            "created": True,
        }
        resp = SetSecretResponse.model_validate(data)
        assert resp.created is True
        assert resp.secret is not None
        assert resp.secret.key == "API_KEY"


class TestListSecretResponse:
    def test_parse(self) -> None:
        data = {
            "secrets": [{"id": "s1", "key": "K1", "scope": "user", "scopeId": "u1"}],
            "scope": "user",
            "scopeId": "u1",
        }
        resp = ListSecretResponse.model_validate(data)
        assert len(resp.secrets) == 1
        assert resp.scope == "user"


class TestMessage:
    def test_parse(self) -> None:
        data = {
            "id": "m1",
            "projectId": "p1",
            "sender": "agent-1",
            "msg": "hello",
            "type": "text",
            "read": False,
        }
        msg = Message.model_validate(data)
        assert msg.id == "m1"
        assert msg.project_id == "p1"
        assert msg.msg == "hello"
        assert msg.read is False


class TestStructuredMessage:
    def test_model_dump_api(self) -> None:
        msg = StructuredMessage(type="notification", content="task done", urgent=True)
        dump = msg.model_dump_api()
        assert dump["type"] == "notification"
        assert dump["content"] == "task done"
        assert dump["urgent"] is True


class TestAgentMessage:
    def test_parse(self) -> None:
        data = {
            "id": "am1",
            "projectId": "p1",
            "msg": "need input",
            "type": "question",
            "read": False,
            "agentId": "a1",
        }
        msg = AgentMessage.model_validate(data)
        assert msg.id == "am1"
        assert msg.agent_id == "a1"

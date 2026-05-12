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

"""Tests for the ProjectsResource and AsyncProjectsResource."""

import httpx
import pytest
import respx

from scion import AsyncScionClient, ScionClient

BASE_URL = "https://hub.example.com"
API_BASE = f"{BASE_URL}/api/v1/projects"


# -- Sample response fixtures --

SAMPLE_PROJECT = {
    "id": "proj-123",
    "name": "My Project",
    "slug": "my-project",
    "gitRemote": "https://github.com/org/repo",
    "visibility": "private",
    "labels": {"env": "dev"},
    "created": "2026-01-01T00:00:00Z",
    "updated": "2026-01-02T00:00:00Z",
    "createdBy": "user-1",
    "ownerId": "user-1",
    "agentCount": 3,
    "activeBrokerCount": 1,
}

SAMPLE_PROJECT_2 = {
    "id": "proj-456",
    "name": "Another Project",
    "slug": "another-project",
    "visibility": "public",
}

SAMPLE_AGENT = {
    "id": "agent-001",
    "slug": "worker-1",
    "name": "Worker 1",
    "projectId": "proj-123",
    "phase": "running",
    "status": "active",
    "containerId": "ctr-abc",
}

SAMPLE_AGENT_2 = {
    "id": "agent-002",
    "slug": "worker-2",
    "name": "Worker 2",
    "projectId": "proj-123",
    "phase": "stopped",
    "status": "stopped",
    "containerId": "ctr-def",
}


# ==================================================================
# Synchronous ProjectsResource tests
# ==================================================================


class TestProjectsResourceList:
    """Tests for ProjectsResource.list()."""

    def test_list_returns_projects(self) -> None:
        with respx.mock:
            respx.get(API_BASE).mock(
                return_value=httpx.Response(
                    200,
                    json={"projects": [SAMPLE_PROJECT, SAMPLE_PROJECT_2]},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list()

            assert len(page) == 2
            assert page.data[0].id == "proj-123"
            assert page.data[0].name == "My Project"
            assert page.data[0].slug == "my-project"
            assert page.data[1].id == "proj-456"
            assert page.has_next is False
            client.close()

    def test_list_with_filters(self) -> None:
        with respx.mock:
            route = respx.get(API_BASE).mock(
                return_value=httpx.Response(
                    200,
                    json={"projects": [SAMPLE_PROJECT]},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list(
                visibility="private",
                name="My Project",
                limit=10,
            )

            assert len(page) == 1
            # Verify query params were sent
            request = route.calls[0].request
            assert "visibility=private" in str(request.url)
            assert "name=My+Project" in str(request.url) or "name=My%20Project" in str(
                request.url
            )
            assert "limit=10" in str(request.url)
            client.close()

    def test_list_empty(self) -> None:
        with respx.mock:
            respx.get(API_BASE).mock(
                return_value=httpx.Response(200, json={"projects": []})
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list()

            assert len(page) == 0
            assert page.has_next is False
            client.close()

    def test_list_pagination(self) -> None:
        with respx.mock:
            # First page
            respx.get(API_BASE, params__contains={"limit": "1"}).mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "projects": [SAMPLE_PROJECT],
                            "nextCursor": "cursor-page2",
                        },
                    ),
                    httpx.Response(
                        200,
                        json={
                            "projects": [SAMPLE_PROJECT_2],
                        },
                    ),
                ]
            )

            client = ScionClient(BASE_URL, token="test-token")
            page1 = client.projects.list(limit=1)

            assert len(page1) == 1
            assert page1.data[0].id == "proj-123"
            assert page1.has_next is True
            assert page1.next_cursor == "cursor-page2"

            # Fetch next page
            page2 = page1.get_next_page()
            assert len(page2) == 1
            assert page2.data[0].id == "proj-456"
            assert page2.has_next is False
            client.close()

    def test_list_auto_paging_iter(self) -> None:
        with respx.mock:
            respx.get(API_BASE).mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "projects": [SAMPLE_PROJECT],
                            "nextCursor": "cursor-2",
                        },
                    ),
                    httpx.Response(
                        200,
                        json={
                            "projects": [SAMPLE_PROJECT_2],
                        },
                    ),
                ]
            )

            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list()

            all_projects = list(page.auto_paging_iter())
            assert len(all_projects) == 2
            assert all_projects[0].id == "proj-123"
            assert all_projects[1].id == "proj-456"
            client.close()

    def test_list_with_git_remote_filter(self) -> None:
        with respx.mock:
            route = respx.get(API_BASE).mock(
                return_value=httpx.Response(
                    200, json={"projects": [SAMPLE_PROJECT]}
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.projects.list(git_remote="https://github.com/org/repo")

            request = route.calls[0].request
            url_str = str(request.url)
            assert "gitRemote=" in url_str
            client.close()

    def test_list_with_labels_filter(self) -> None:
        with respx.mock:
            route = respx.get(API_BASE).mock(
                return_value=httpx.Response(
                    200, json={"projects": [SAMPLE_PROJECT]}
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.projects.list(labels={"env": "prod"})

            request = route.calls[0].request
            url_str = str(request.url)
            assert "label=env%3Dprod" in url_str or "label=env=prod" in url_str
            client.close()


class TestProjectsResourceGet:
    """Tests for ProjectsResource.get()."""

    def test_get_project(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(200, json=SAMPLE_PROJECT)
            )
            client = ScionClient(BASE_URL, token="test-token")
            project = client.projects.get("proj-123")

            assert project.id == "proj-123"
            assert project.name == "My Project"
            assert project.slug == "my-project"
            assert project.git_remote == "https://github.com/org/repo"
            assert project.visibility == "private"
            assert project.labels == {"env": "dev"}
            assert project.agent_count == 3
            client.close()

    def test_get_project_not_found(self) -> None:
        from scion import NotFoundError

        with respx.mock:
            respx.get(f"{API_BASE}/nonexistent").mock(
                return_value=httpx.Response(
                    404,
                    json={
                        "error": {
                            "code": "not_found",
                            "message": "Project not found",
                        }
                    },
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            with pytest.raises(NotFoundError):
                client.projects.get("nonexistent")
            client.close()


class TestProjectsResourceCreate:
    """Tests for ProjectsResource.create()."""

    def test_create_project(self) -> None:
        with respx.mock:
            route = respx.post(API_BASE).mock(
                return_value=httpx.Response(200, json=SAMPLE_PROJECT)
            )
            client = ScionClient(BASE_URL, token="test-token")
            project = client.projects.create(
                name="My Project",
                git_remote="https://github.com/org/repo",
                visibility="private",
                labels={"env": "dev"},
            )

            assert project.id == "proj-123"
            assert project.name == "My Project"

            # Verify the request body
            import json

            request_body = json.loads(route.calls[0].request.content)
            assert request_body["name"] == "My Project"
            assert request_body["gitRemote"] == "https://github.com/org/repo"
            assert request_body["visibility"] == "private"
            assert request_body["labels"] == {"env": "dev"}
            client.close()

    def test_create_project_minimal(self) -> None:
        with respx.mock:
            route = respx.post(API_BASE).mock(
                return_value=httpx.Response(
                    200,
                    json={"id": "proj-new", "name": "Minimal", "slug": "minimal"},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            project = client.projects.create(name="Minimal")

            assert project.id == "proj-new"
            assert project.name == "Minimal"

            import json

            request_body = json.loads(route.calls[0].request.content)
            assert request_body["name"] == "Minimal"
            # Optional fields should not be in the request
            assert "gitRemote" not in request_body
            assert "visibility" not in request_body
            client.close()

    def test_create_project_with_slug_and_id(self) -> None:
        with respx.mock:
            route = respx.post(API_BASE).mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "id": "custom-id",
                        "name": "Custom",
                        "slug": "custom-slug",
                    },
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            project = client.projects.create(
                name="Custom", slug="custom-slug", id="custom-id"
            )

            assert project.id == "custom-id"
            assert project.slug == "custom-slug"

            import json

            request_body = json.loads(route.calls[0].request.content)
            assert request_body["slug"] == "custom-slug"
            assert request_body["id"] == "custom-id"
            client.close()


class TestProjectsResourceUpdate:
    """Tests for ProjectsResource.update()."""

    def test_update_project(self) -> None:
        updated_project = {**SAMPLE_PROJECT, "name": "Updated Name"}
        with respx.mock:
            route = respx.patch(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(200, json=updated_project)
            )
            client = ScionClient(BASE_URL, token="test-token")
            project = client.projects.update(
                "proj-123",
                name="Updated Name",
                visibility="public",
            )

            assert project.name == "Updated Name"

            import json

            request_body = json.loads(route.calls[0].request.content)
            assert request_body["name"] == "Updated Name"
            assert request_body["visibility"] == "public"
            client.close()

    def test_update_project_labels(self) -> None:
        updated_project = {**SAMPLE_PROJECT, "labels": {"env": "prod"}}
        with respx.mock:
            route = respx.patch(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(200, json=updated_project)
            )
            client = ScionClient(BASE_URL, token="test-token")
            project = client.projects.update(
                "proj-123",
                labels={"env": "prod"},
            )

            assert project.labels == {"env": "prod"}

            import json

            request_body = json.loads(route.calls[0].request.content)
            assert request_body["labels"] == {"env": "prod"}
            client.close()

    def test_update_project_not_found(self) -> None:
        from scion import NotFoundError

        with respx.mock:
            respx.patch(f"{API_BASE}/nonexistent").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "Not found"}},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            with pytest.raises(NotFoundError):
                client.projects.update("nonexistent", name="X")
            client.close()


class TestProjectsResourceDelete:
    """Tests for ProjectsResource.delete()."""

    def test_delete_project(self) -> None:
        with respx.mock:
            route = respx.delete(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(204)
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.projects.delete("proj-123")

            assert route.called
            client.close()

    def test_delete_project_not_found(self) -> None:
        from scion import NotFoundError

        with respx.mock:
            respx.delete(f"{API_BASE}/nonexistent").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "Not found"}},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            with pytest.raises(NotFoundError):
                client.projects.delete("nonexistent")
            client.close()


class TestProjectsResourceListAgents:
    """Tests for ProjectsResource.list_agents()."""

    def test_list_agents(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={"agents": [SAMPLE_AGENT, SAMPLE_AGENT_2]},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list_agents("proj-123")

            assert len(page) == 2
            assert page.data[0].id == "agent-001"
            assert page.data[0].name == "Worker 1"
            assert page.data[0].phase == "running"
            assert page.data[1].id == "agent-002"
            assert page.has_next is False
            client.close()

    def test_list_agents_with_filters(self) -> None:
        with respx.mock:
            route = respx.get(f"{API_BASE}/proj-123/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={"agents": [SAMPLE_AGENT]},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list_agents(
                "proj-123",
                phase="running",
                limit=5,
            )

            assert len(page) == 1
            request = route.calls[0].request
            assert "phase=running" in str(request.url)
            assert "limit=5" in str(request.url)
            client.close()

    def test_list_agents_pagination(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123/agents").mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "agents": [SAMPLE_AGENT],
                            "nextCursor": "agent-cursor-2",
                        },
                    ),
                    httpx.Response(
                        200,
                        json={"agents": [SAMPLE_AGENT_2]},
                    ),
                ]
            )

            client = ScionClient(BASE_URL, token="test-token")
            page1 = client.projects.list_agents("proj-123")

            assert len(page1) == 1
            assert page1.has_next is True

            page2 = page1.get_next_page()
            assert len(page2) == 1
            assert page2.data[0].id == "agent-002"
            assert page2.has_next is False
            client.close()

    def test_list_agents_empty(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123/agents").mock(
                return_value=httpx.Response(200, json={"agents": []})
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.projects.list_agents("proj-123")

            assert len(page) == 0
            assert page.has_next is False
            client.close()


class TestProjectsResourceClientProperty:
    """Tests for the client.projects property wiring."""

    def test_projects_property_exists(self) -> None:
        client = ScionClient(BASE_URL, token="test-token")
        assert hasattr(client, "projects")
        # Accessing twice returns the same instance (cached)
        p1 = client.projects
        p2 = client.projects
        assert p1 is p2
        client.close()


class TestProjectsResourceErrorCases:
    """Tests for error handling in ProjectsResource."""

    def test_authentication_error(self) -> None:
        from scion import AuthenticationError

        with respx.mock:
            respx.get(API_BASE).mock(
                return_value=httpx.Response(
                    401,
                    json={"error": {"code": "unauthorized", "message": "Invalid token"}},
                )
            )
            client = ScionClient(BASE_URL, token="bad-token")
            with pytest.raises(AuthenticationError):
                client.projects.list()
            client.close()

    def test_server_error(self) -> None:
        from scion import ServerError

        with respx.mock:
            respx.get(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(
                    500,
                    json={"error": {"code": "internal_error", "message": "Server error"}},
                )
            )
            client = ScionClient(BASE_URL, token="test-token", max_retries=0)
            with pytest.raises(ServerError):
                client.projects.get("proj-123")
            client.close()

    def test_validation_error_on_create(self) -> None:
        from scion import ValidationError

        with respx.mock:
            respx.post(API_BASE).mock(
                return_value=httpx.Response(
                    400,
                    json={
                        "error": {
                            "code": "validation_error",
                            "message": "Name is required",
                        }
                    },
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            with pytest.raises(ValidationError):
                client.projects.create(name="")
            client.close()


# ==================================================================
# Async ProjectsResource tests
# ==================================================================


class TestAsyncProjectsResourceList:
    """Tests for AsyncProjectsResource.list()."""

    @pytest.mark.asyncio
    async def test_list_returns_projects(self) -> None:
        with respx.mock:
            respx.get(API_BASE).mock(
                return_value=httpx.Response(
                    200,
                    json={"projects": [SAMPLE_PROJECT, SAMPLE_PROJECT_2]},
                )
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            page = await client.projects.list()

            assert len(page) == 2
            assert page.data[0].id == "proj-123"
            assert page.data[1].id == "proj-456"
            await client.close()

    @pytest.mark.asyncio
    async def test_list_pagination(self) -> None:
        with respx.mock:
            respx.get(API_BASE).mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "projects": [SAMPLE_PROJECT],
                            "nextCursor": "cursor-2",
                        },
                    ),
                    httpx.Response(
                        200,
                        json={"projects": [SAMPLE_PROJECT_2]},
                    ),
                ]
            )

            client = AsyncScionClient(BASE_URL, token="test-token")
            page1 = await client.projects.list()

            assert page1.has_next is True
            page2 = await page1.get_next_page()
            assert page2.data[0].id == "proj-456"
            assert page2.has_next is False
            await client.close()

    @pytest.mark.asyncio
    async def test_list_auto_paging_iter(self) -> None:
        with respx.mock:
            respx.get(API_BASE).mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "projects": [SAMPLE_PROJECT],
                            "nextCursor": "cursor-2",
                        },
                    ),
                    httpx.Response(
                        200,
                        json={"projects": [SAMPLE_PROJECT_2]},
                    ),
                ]
            )

            client = AsyncScionClient(BASE_URL, token="test-token")
            page = await client.projects.list()

            all_projects = []
            async for project in page.auto_paging_iter():
                all_projects.append(project)

            assert len(all_projects) == 2
            assert all_projects[0].id == "proj-123"
            assert all_projects[1].id == "proj-456"
            await client.close()


class TestAsyncProjectsResourceGet:
    """Tests for AsyncProjectsResource.get()."""

    @pytest.mark.asyncio
    async def test_get_project(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(200, json=SAMPLE_PROJECT)
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            project = await client.projects.get("proj-123")

            assert project.id == "proj-123"
            assert project.name == "My Project"
            await client.close()


class TestAsyncProjectsResourceCreate:
    """Tests for AsyncProjectsResource.create()."""

    @pytest.mark.asyncio
    async def test_create_project(self) -> None:
        with respx.mock:
            respx.post(API_BASE).mock(
                return_value=httpx.Response(200, json=SAMPLE_PROJECT)
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            project = await client.projects.create(
                name="My Project",
                visibility="private",
            )

            assert project.id == "proj-123"
            await client.close()


class TestAsyncProjectsResourceUpdate:
    """Tests for AsyncProjectsResource.update()."""

    @pytest.mark.asyncio
    async def test_update_project(self) -> None:
        updated = {**SAMPLE_PROJECT, "name": "Updated"}
        with respx.mock:
            respx.patch(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(200, json=updated)
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            project = await client.projects.update("proj-123", name="Updated")

            assert project.name == "Updated"
            await client.close()


class TestAsyncProjectsResourceDelete:
    """Tests for AsyncProjectsResource.delete()."""

    @pytest.mark.asyncio
    async def test_delete_project(self) -> None:
        with respx.mock:
            route = respx.delete(f"{API_BASE}/proj-123").mock(
                return_value=httpx.Response(204)
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            await client.projects.delete("proj-123")

            assert route.called
            await client.close()


class TestAsyncProjectsResourceListAgents:
    """Tests for AsyncProjectsResource.list_agents()."""

    @pytest.mark.asyncio
    async def test_list_agents(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123/agents").mock(
                return_value=httpx.Response(
                    200,
                    json={"agents": [SAMPLE_AGENT]},
                )
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            page = await client.projects.list_agents("proj-123")

            assert len(page) == 1
            assert page.data[0].id == "agent-001"
            await client.close()

    @pytest.mark.asyncio
    async def test_list_agents_pagination(self) -> None:
        with respx.mock:
            respx.get(f"{API_BASE}/proj-123/agents").mock(
                side_effect=[
                    httpx.Response(
                        200,
                        json={
                            "agents": [SAMPLE_AGENT],
                            "nextCursor": "agent-cursor-2",
                        },
                    ),
                    httpx.Response(
                        200,
                        json={"agents": [SAMPLE_AGENT_2]},
                    ),
                ]
            )

            client = AsyncScionClient(BASE_URL, token="test-token")
            page1 = await client.projects.list_agents("proj-123")
            assert page1.has_next is True

            page2 = await page1.get_next_page()
            assert page2.data[0].id == "agent-002"
            assert page2.has_next is False
            await client.close()


class TestAsyncProjectsResourceClientProperty:
    """Tests for the async client.projects property wiring."""

    def test_projects_property_exists(self) -> None:
        client = AsyncScionClient(BASE_URL, token="test-token")
        assert hasattr(client, "projects")
        p1 = client.projects
        p2 = client.projects
        assert p1 is p2

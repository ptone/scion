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

"""Tests for the SecretsResource and AsyncSecretsResource."""

import httpx
import pytest
import respx

from scion import AsyncScionClient, ScionClient
from scion._errors import NotFoundError, ValidationError

BASE_URL = "https://hub.example.com"
TOKEN = "test-token"

# -- Fixtures / sample data ---------------------------------------------------

SAMPLE_SECRET = {
    "id": "sec-001",
    "key": "MY_API_KEY",
    "secretRef": "refs/secrets/sec-001",
    "type": "environment",
    "target": "MY_API_KEY",
    "scope": "user",
    "scopeId": "",
    "description": "API key for external service",
    "injectionMode": "as_needed",
    "allowProgeny": False,
    "version": 1,
    "created": "2026-01-15T10:00:00Z",
    "updated": "2026-01-15T10:00:00Z",
    "createdBy": "user-123",
    "updatedBy": "user-123",
}

SAMPLE_SECRET_PROJECT = {
    "id": "sec-002",
    "key": "DB_PASSWORD",
    "type": "environment",
    "scope": "project",
    "scopeId": "proj-456",
    "version": 2,
    "created": "2026-02-01T08:00:00Z",
    "updated": "2026-03-01T12:00:00Z",
}


# ==============================================================================
# Synchronous client tests
# ==============================================================================


class TestSecretsResourceList:
    """Tests for SecretsResource.list()."""

    def test_list_no_scope(self) -> None:
        """List secrets without scope parameters."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/secrets").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secrets": [SAMPLE_SECRET],
                        "scope": "user",
                        "scopeId": "",
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.list()

            assert len(result.secrets) == 1
            assert result.secrets[0].key == "MY_API_KEY"
            assert result.secrets[0].id == "sec-001"
            assert result.scope == "user"
            # No query params should be sent
            assert route.calls[0].request.url.params.keys() == set()
            client.close()

    def test_list_with_scope(self) -> None:
        """List secrets with scope and scope_id parameters."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/secrets").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secrets": [SAMPLE_SECRET_PROJECT],
                        "scope": "project",
                        "scopeId": "proj-456",
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.list(scope="project", scope_id="proj-456")

            assert len(result.secrets) == 1
            assert result.secrets[0].scope == "project"
            assert result.secrets[0].scope_id == "proj-456"
            assert result.scope_id == "proj-456"
            # Verify query params
            assert route.calls[0].request.url.params["scope"] == "project"
            assert route.calls[0].request.url.params["scopeId"] == "proj-456"
            client.close()

    def test_list_empty(self) -> None:
        """List returns empty when no secrets exist."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/secrets").mock(
                return_value=httpx.Response(
                    200,
                    json={"secrets": [], "scope": "user", "scopeId": ""},
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.list()
            assert result.secrets == []
            client.close()

    def test_list_scope_only(self) -> None:
        """List with scope but no scope_id sends only scope param."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/secrets").mock(
                return_value=httpx.Response(
                    200,
                    json={"secrets": [], "scope": "user", "scopeId": ""},
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            client.secrets.list(scope="user")
            params = route.calls[0].request.url.params
            assert params["scope"] == "user"
            assert "scopeId" not in params
            client.close()


class TestSecretsResourceGet:
    """Tests for SecretsResource.get()."""

    def test_get_basic(self) -> None:
        """Get a secret by key."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/secrets/MY_API_KEY").mock(
                return_value=httpx.Response(200, json=SAMPLE_SECRET)
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            secret = client.secrets.get("MY_API_KEY")

            assert secret.key == "MY_API_KEY"
            assert secret.id == "sec-001"
            assert secret.secret_ref == "refs/secrets/sec-001"
            assert secret.secret_type == "environment"
            assert secret.injection_mode == "as_needed"
            assert secret.allow_progeny is False
            assert secret.version == 1
            assert secret.created_by == "user-123"
            assert route.calls[0].request.url.params.keys() == set()
            client.close()

    def test_get_with_scope(self) -> None:
        """Get a secret with scope parameters."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/secrets/DB_PASSWORD").mock(
                return_value=httpx.Response(200, json=SAMPLE_SECRET_PROJECT)
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            secret = client.secrets.get(
                "DB_PASSWORD", scope="project", scope_id="proj-456"
            )

            assert secret.key == "DB_PASSWORD"
            assert secret.scope == "project"
            assert route.calls[0].request.url.params["scope"] == "project"
            assert route.calls[0].request.url.params["scopeId"] == "proj-456"
            client.close()

    def test_get_url_encodes_key(self) -> None:
        """Keys with special characters are URL-encoded in the path."""
        with respx.mock:
            route = respx.get(url__regex=r".*/api/v1/secrets/.*").mock(
                return_value=httpx.Response(
                    200, json={"key": "my/special key", "scope": "user", "scopeId": ""}
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            client.secrets.get("my/special key")

            # Verify the raw URL contains URL-encoded key
            raw_url = str(route.calls[0].request.url)
            assert "my%2Fspecial%20key" in raw_url
            client.close()

    def test_get_not_found(self) -> None:
        """Get raises NotFoundError for 404 response."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/secrets/MISSING").mock(
                return_value=httpx.Response(
                    404,
                    json={
                        "error": {
                            "code": "not_found",
                            "message": "secret not found",
                        }
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            with pytest.raises(NotFoundError):
                client.secrets.get("MISSING")
            client.close()


class TestSecretsResourceSet:
    """Tests for SecretsResource.set()."""

    def test_set_basic(self) -> None:
        """Create a new secret with minimal fields."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/secrets/NEW_KEY").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secret": {
                            "id": "sec-new",
                            "key": "NEW_KEY",
                            "scope": "user",
                            "scopeId": "",
                            "version": 1,
                        },
                        "created": True,
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.set("NEW_KEY", "my-secret-value")

            assert result.created is True
            assert result.secret is not None
            assert result.secret.key == "NEW_KEY"
            # Verify the request body contains the value
            body = route.calls[0].request.content
            import json

            parsed = json.loads(body)
            assert parsed["value"] == "my-secret-value"
            client.close()

    def test_set_with_all_options(self) -> None:
        """Create a secret with all optional fields."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/secrets/FULL_KEY").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secret": {
                            "id": "sec-full",
                            "key": "FULL_KEY",
                            "scope": "project",
                            "scopeId": "proj-789",
                            "type": "file",
                            "target": "/etc/secret.conf",
                            "injectionMode": "always",
                            "allowProgeny": True,
                            "description": "Config file",
                            "version": 1,
                        },
                        "created": True,
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.set(
                "FULL_KEY",
                "secret-file-content",
                scope="project",
                scope_id="proj-789",
                description="Config file",
                injection_mode="always",
                secret_type="file",
                target="/etc/secret.conf",
                allow_progeny=True,
            )

            assert result.created is True
            assert result.secret is not None
            assert result.secret.secret_type == "file"

            # Verify the request body
            import json

            parsed = json.loads(route.calls[0].request.content)
            assert parsed["value"] == "secret-file-content"
            assert parsed["scope"] == "project"
            assert parsed["scopeId"] == "proj-789"
            assert parsed["description"] == "Config file"
            assert parsed["injectionMode"] == "always"
            assert parsed["type"] == "file"
            assert parsed["target"] == "/etc/secret.conf"
            assert parsed["allowProgeny"] is True
            client.close()

    def test_set_update_existing(self) -> None:
        """Update an existing secret returns created=False."""
        with respx.mock:
            respx.put(f"{BASE_URL}/api/v1/secrets/EXISTING").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secret": {
                            "id": "sec-exist",
                            "key": "EXISTING",
                            "scope": "user",
                            "scopeId": "",
                            "version": 3,
                        },
                        "created": False,
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.set("EXISTING", "updated-value")
            assert result.created is False
            assert result.secret is not None
            assert result.secret.version == 3
            client.close()

    def test_set_excludes_none_fields(self) -> None:
        """Optional fields set to None are excluded from the request body."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/secrets/MINIMAL").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secret": {"key": "MINIMAL", "scope": "user", "scopeId": ""},
                        "created": True,
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            client.secrets.set("MINIMAL", "val")

            import json

            parsed = json.loads(route.calls[0].request.content)
            assert "value" in parsed
            # None fields should not be present
            assert "description" not in parsed
            assert "injectionMode" not in parsed
            assert "type" not in parsed
            assert "target" not in parsed
            assert "allowProgeny" not in parsed
            assert "scope" not in parsed
            assert "scopeId" not in parsed
            client.close()

    def test_set_validation_error(self) -> None:
        """Set raises ValidationError for 400 response."""
        with respx.mock:
            respx.put(f"{BASE_URL}/api/v1/secrets/BAD").mock(
                return_value=httpx.Response(
                    400,
                    json={
                        "error": {
                            "code": "validation_error",
                            "message": "key contains invalid characters",
                        }
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            with pytest.raises(ValidationError):
                client.secrets.set("BAD", "value")
            client.close()


class TestSecretsResourceDelete:
    """Tests for SecretsResource.delete()."""

    def test_delete_basic(self) -> None:
        """Delete a secret without scope parameters."""
        with respx.mock:
            route = respx.delete(f"{BASE_URL}/api/v1/secrets/OLD_KEY").mock(
                return_value=httpx.Response(204)
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            client.secrets.delete("OLD_KEY")

            assert route.called
            assert route.calls[0].request.url.params.keys() == set()
            client.close()

    def test_delete_with_scope(self) -> None:
        """Delete a secret with scope parameters."""
        with respx.mock:
            route = respx.delete(f"{BASE_URL}/api/v1/secrets/PROJ_KEY").mock(
                return_value=httpx.Response(204)
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            client.secrets.delete("PROJ_KEY", scope="project", scope_id="proj-456")

            assert route.calls[0].request.url.params["scope"] == "project"
            assert route.calls[0].request.url.params["scopeId"] == "proj-456"
            client.close()

    def test_delete_not_found(self) -> None:
        """Delete raises NotFoundError for 404 response."""
        with respx.mock:
            respx.delete(f"{BASE_URL}/api/v1/secrets/GONE").mock(
                return_value=httpx.Response(
                    404,
                    json={
                        "error": {
                            "code": "not_found",
                            "message": "secret not found",
                        }
                    },
                )
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            with pytest.raises(NotFoundError):
                client.secrets.delete("GONE")
            client.close()

    def test_delete_returns_none(self) -> None:
        """Delete returns None on success."""
        with respx.mock:
            respx.delete(f"{BASE_URL}/api/v1/secrets/KEY").mock(
                return_value=httpx.Response(204)
            )
            client = ScionClient(BASE_URL, token=TOKEN)
            result = client.secrets.delete("KEY")
            assert result is None
            client.close()


class TestSecretsResourceProperty:
    """Tests for the client.secrets property."""

    def test_secrets_property_returns_resource(self) -> None:
        """The secrets property returns a SecretsResource instance."""
        from scion.resources.secrets import SecretsResource

        client = ScionClient(BASE_URL, token=TOKEN)
        assert isinstance(client.secrets, SecretsResource)
        client.close()

    def test_secrets_property_cached(self) -> None:
        """The secrets property returns the same instance on repeated access."""
        client = ScionClient(BASE_URL, token=TOKEN)
        s1 = client.secrets
        s2 = client.secrets
        assert s1 is s2
        client.close()


# ==============================================================================
# Asynchronous client tests
# ==============================================================================


class TestAsyncSecretsResourceList:
    """Tests for AsyncSecretsResource.list()."""

    @pytest.mark.asyncio
    async def test_list_no_scope(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/secrets").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secrets": [SAMPLE_SECRET],
                        "scope": "user",
                        "scopeId": "",
                    },
                )
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            result = await client.secrets.list()
            assert len(result.secrets) == 1
            assert result.secrets[0].key == "MY_API_KEY"
            await client.close()

    @pytest.mark.asyncio
    async def test_list_with_scope(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/secrets").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secrets": [SAMPLE_SECRET_PROJECT],
                        "scope": "project",
                        "scopeId": "proj-456",
                    },
                )
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            result = await client.secrets.list(
                scope="project", scope_id="proj-456"
            )
            assert len(result.secrets) == 1
            assert route.calls[0].request.url.params["scope"] == "project"
            await client.close()


class TestAsyncSecretsResourceGet:
    """Tests for AsyncSecretsResource.get()."""

    @pytest.mark.asyncio
    async def test_get_basic(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/secrets/MY_API_KEY").mock(
                return_value=httpx.Response(200, json=SAMPLE_SECRET)
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            secret = await client.secrets.get("MY_API_KEY")
            assert secret.key == "MY_API_KEY"
            assert secret.id == "sec-001"
            await client.close()

    @pytest.mark.asyncio
    async def test_get_not_found(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/secrets/MISSING").mock(
                return_value=httpx.Response(
                    404,
                    json={
                        "error": {
                            "code": "not_found",
                            "message": "secret not found",
                        }
                    },
                )
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            with pytest.raises(NotFoundError):
                await client.secrets.get("MISSING")
            await client.close()


class TestAsyncSecretsResourceSet:
    """Tests for AsyncSecretsResource.set()."""

    @pytest.mark.asyncio
    async def test_set_basic(self) -> None:
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/secrets/ASYNC_KEY").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secret": {
                            "id": "sec-async",
                            "key": "ASYNC_KEY",
                            "scope": "user",
                            "scopeId": "",
                            "version": 1,
                        },
                        "created": True,
                    },
                )
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            result = await client.secrets.set("ASYNC_KEY", "async-value")
            assert result.created is True
            assert result.secret is not None
            assert result.secret.key == "ASYNC_KEY"

            import json

            parsed = json.loads(route.calls[0].request.content)
            assert parsed["value"] == "async-value"
            await client.close()

    @pytest.mark.asyncio
    async def test_set_with_all_options(self) -> None:
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/secrets/FULL_ASYNC").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "secret": {
                            "key": "FULL_ASYNC",
                            "scope": "project",
                            "scopeId": "proj-100",
                        },
                        "created": True,
                    },
                )
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            await client.secrets.set(
                "FULL_ASYNC",
                "val",
                scope="project",
                scope_id="proj-100",
                description="test",
                injection_mode="always",
                secret_type="variable",
                target="MY_VAR",
                allow_progeny=True,
            )

            import json

            parsed = json.loads(route.calls[0].request.content)
            assert parsed["scope"] == "project"
            assert parsed["scopeId"] == "proj-100"
            assert parsed["injectionMode"] == "always"
            assert parsed["type"] == "variable"
            await client.close()


class TestAsyncSecretsResourceDelete:
    """Tests for AsyncSecretsResource.delete()."""

    @pytest.mark.asyncio
    async def test_delete_basic(self) -> None:
        with respx.mock:
            route = respx.delete(f"{BASE_URL}/api/v1/secrets/ASYNC_DEL").mock(
                return_value=httpx.Response(204)
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            result = await client.secrets.delete("ASYNC_DEL")
            assert result is None
            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_delete_with_scope(self) -> None:
        with respx.mock:
            route = respx.delete(f"{BASE_URL}/api/v1/secrets/ASYNC_DEL").mock(
                return_value=httpx.Response(204)
            )
            client = AsyncScionClient(BASE_URL, token=TOKEN)
            await client.secrets.delete(
                "ASYNC_DEL", scope="project", scope_id="proj-456"
            )
            assert route.calls[0].request.url.params["scope"] == "project"
            assert route.calls[0].request.url.params["scopeId"] == "proj-456"
            await client.close()


class TestAsyncSecretsResourceProperty:
    """Tests for the async client.secrets property."""

    def test_secrets_property_returns_resource(self) -> None:
        from scion.resources.secrets import AsyncSecretsResource

        client = AsyncScionClient(BASE_URL, token=TOKEN)
        assert isinstance(client.secrets, AsyncSecretsResource)

    def test_secrets_property_cached(self) -> None:
        client = AsyncScionClient(BASE_URL, token=TOKEN)
        s1 = client.secrets
        s2 = client.secrets
        assert s1 is s2

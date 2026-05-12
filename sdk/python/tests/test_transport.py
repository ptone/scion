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

"""Tests for the HTTP transport layer."""

import httpx
import pytest
import respx

from scion._errors import (
    AuthenticationError,
    NotFoundError,
    ServerError,
    ValidationError,
)
from scion._transport import (
    DEFAULT_USER_AGENT,
    AsyncTransport,
    Transport,
)

BASE_URL = "https://hub.example.com"


class TestTransport:
    def test_get_success(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = Transport(BASE_URL, max_retries=0)
            resp = transport.get("/healthz")
            assert resp.status_code == 200
            assert resp.json()["status"] == "ok"
            transport.close()

    def test_post_with_json(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(201, json={"agent": {"id": "a1"}})
            )
            transport = Transport(BASE_URL, max_retries=0)
            resp = transport.post("/api/v1/agents", json={"name": "test"})
            assert resp.status_code == 201
            transport.close()

    def test_auth_header_injection(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = Transport(BASE_URL, token="my-token", max_retries=0)
            transport.get("/healthz")
            assert route.calls[0].request.headers["authorization"] == "Bearer my-token"
            transport.close()

    def test_user_agent_header(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = Transport(BASE_URL, max_retries=0)
            transport.get("/healthz")
            assert route.calls[0].request.headers["user-agent"] == DEFAULT_USER_AGENT
            transport.close()

    def test_4xx_raises_no_retry(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/agents/bad").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "not found"}},
                )
            )
            transport = Transport(BASE_URL, max_retries=3)
            with pytest.raises(NotFoundError):
                transport.get("/api/v1/agents/bad")
            # Should NOT retry on 4xx — only 1 call
            assert len(route.calls) == 1
            transport.close()

    def test_400_raises_validation_error(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/agents").mock(
                return_value=httpx.Response(
                    400,
                    json={"error": {"code": "validation_error", "message": "bad request"}},
                )
            )
            transport = Transport(BASE_URL, max_retries=3)
            with pytest.raises(ValidationError):
                transport.post("/api/v1/agents", json={"name": ""})
            transport.close()

    def test_401_raises_auth_error(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(
                    401,
                    json={"error": {"code": "unauthorized", "message": "bad token"}},
                )
            )
            transport = Transport(BASE_URL, max_retries=0)
            with pytest.raises(AuthenticationError):
                transport.get("/healthz")
            transport.close()

    def test_5xx_retries_then_raises(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(
                    503,
                    json={"error": {"code": "unavailable", "message": "down"}},
                )
            )
            transport = Transport(BASE_URL, max_retries=2)
            with pytest.raises(ServerError):
                transport.get("/healthz")
            # Should have retried: 1 initial + 2 retries = 3 total
            assert len(route.calls) == 3
            transport.close()

    def test_5xx_recovery_on_retry(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz")
            route.side_effect = [
                httpx.Response(503, json={"error": {"code": "unavailable", "message": "down"}}),
                httpx.Response(200, json={"status": "ok"}),
            ]
            transport = Transport(BASE_URL, max_retries=2)
            resp = transport.get("/healthz")
            assert resp.status_code == 200
            assert len(route.calls) == 2
            transport.close()

    def test_put_method(self) -> None:
        with respx.mock:
            respx.put(f"{BASE_URL}/api/v1/secrets/key1").mock(
                return_value=httpx.Response(200, json={"secret": {"id": "s1"}})
            )
            transport = Transport(BASE_URL, max_retries=0)
            resp = transport.put("/api/v1/secrets/key1", json={"value": "v"})
            assert resp.status_code == 200
            transport.close()

    def test_patch_method(self) -> None:
        with respx.mock:
            respx.patch(f"{BASE_URL}/api/v1/agents/a1").mock(
                return_value=httpx.Response(200, json={"id": "a1"})
            )
            transport = Transport(BASE_URL, max_retries=0)
            resp = transport.patch("/api/v1/agents/a1", json={"name": "new"})
            assert resp.status_code == 200
            transport.close()

    def test_delete_method(self) -> None:
        with respx.mock:
            respx.delete(f"{BASE_URL}/api/v1/agents/a1").mock(
                return_value=httpx.Response(204)
            )
            transport = Transport(BASE_URL, max_retries=0)
            resp = transport.delete("/api/v1/agents/a1")
            assert resp.status_code == 204
            transport.close()

    def test_custom_headers(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = Transport(BASE_URL, max_retries=0, headers={"X-Custom": "val"})
            transport.get("/healthz")
            assert route.calls[0].request.headers["x-custom"] == "val"
            transport.close()

    def test_url_construction_trailing_slash(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = Transport(f"{BASE_URL}/", max_retries=0)
            resp = transport.get("/healthz")
            assert resp.status_code == 200
            transport.close()

    def test_context_manager(self) -> None:
        """Transport close is safe to call multiple times."""
        transport = Transport(BASE_URL, max_retries=0)
        transport.close()
        transport.close()  # Should not raise


class TestAsyncTransport:
    @pytest.mark.asyncio
    async def test_get_success(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = AsyncTransport(BASE_URL, max_retries=0)
            resp = await transport.get("/healthz")
            assert resp.status_code == 200
            assert resp.json()["status"] == "ok"
            await transport.close()

    @pytest.mark.asyncio
    async def test_auth_header_injection(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            transport = AsyncTransport(BASE_URL, token="async-token", max_retries=0)
            await transport.get("/healthz")
            assert route.calls[0].request.headers["authorization"] == "Bearer async-token"
            await transport.close()

    @pytest.mark.asyncio
    async def test_4xx_no_retry(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/agents/bad").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "not found"}},
                )
            )
            transport = AsyncTransport(BASE_URL, max_retries=3)
            with pytest.raises(NotFoundError):
                await transport.get("/api/v1/agents/bad")
            assert len(route.calls) == 1
            await transport.close()

    @pytest.mark.asyncio
    async def test_5xx_retries(self) -> None:
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(
                    500,
                    json={"error": {"code": "internal_error", "message": "fail"}},
                )
            )
            transport = AsyncTransport(BASE_URL, max_retries=1)
            with pytest.raises(ServerError):
                await transport.get("/healthz")
            assert len(route.calls) == 2  # 1 initial + 1 retry
            await transport.close()

    @pytest.mark.asyncio
    async def test_post_with_json(self) -> None:
        with respx.mock:
            respx.post(f"{BASE_URL}/api/v1/projects").mock(
                return_value=httpx.Response(201, json={"id": "p1", "name": "test"})
            )
            transport = AsyncTransport(BASE_URL, max_retries=0)
            resp = await transport.post("/api/v1/projects", json={"name": "test"})
            assert resp.status_code == 201
            await transport.close()

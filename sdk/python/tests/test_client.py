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

"""Tests for the ScionClient and AsyncScionClient."""

import os
from pathlib import Path
from unittest.mock import patch

import httpx
import pytest
import respx

from scion import AsyncScionClient, ScionClient

BASE_URL = "https://hub.example.com"


class TestScionClient:
    def test_health(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "status": "ok",
                        "version": "1.0.0",
                        "scionVersion": "0.5.0",
                        "uptime": "24h",
                    },
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            health = client.health()
            assert health.status == "ok"
            assert health.version == "1.0.0"
            client.close()

    def test_context_manager(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            with ScionClient(BASE_URL, token="test") as client:
                health = client.health()
                assert health.status == "ok"

    def test_token_resolution_explicit(self) -> None:
        """Explicit token takes priority over environment variables."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            with patch.dict(os.environ, {"SCION_API_TOKEN": "env-token"}):
                client = ScionClient(BASE_URL, token="explicit-token")
                client.health()
                assert route.calls[0].request.headers["authorization"] == "Bearer explicit-token"
                client.close()

    def test_token_resolution_api_token_env(self) -> None:
        """SCION_API_TOKEN env var is used when no explicit token."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            env = {"SCION_API_TOKEN": "api-env-token"}
            with patch.dict(os.environ, env, clear=False):
                # Clear DEV_TOKEN to ensure API_TOKEN is picked
                os.environ.pop("SCION_DEV_TOKEN", None)
                client = ScionClient(BASE_URL)
                client.health()
                assert route.calls[0].request.headers["authorization"] == "Bearer api-env-token"
                client.close()

    def test_token_resolution_dev_token_env(self) -> None:
        """SCION_DEV_TOKEN env var is used when SCION_API_TOKEN is not set."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            env = {"SCION_DEV_TOKEN": "dev-env-token"}
            with patch.dict(os.environ, env, clear=False):
                os.environ.pop("SCION_API_TOKEN", None)
                client = ScionClient(BASE_URL)
                client.health()
                assert route.calls[0].request.headers["authorization"] == "Bearer dev-env-token"
                client.close()

    def test_token_resolution_file(self, tmp_path: Path) -> None:
        """Token file ~/.scion/dev-token is used as last resort."""
        token_dir = tmp_path / ".scion"
        token_dir.mkdir()
        token_file = token_dir / "dev-token"
        token_file.write_text("file-token\n")

        with respx.mock:
            route = respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            with patch.dict(os.environ, {}, clear=False):
                os.environ.pop("SCION_API_TOKEN", None)
                os.environ.pop("SCION_DEV_TOKEN", None)
                with patch("scion._client.Path.home", return_value=tmp_path):
                    client = ScionClient(BASE_URL)
                    client.health()
                    assert route.calls[0].request.headers["authorization"] == "Bearer file-token"
                    client.close()

    def test_from_agent_env(self) -> None:
        """from_agent_env() reads SCION_HUB_URL and SCION_AGENT_TOKEN."""
        with respx.mock:
            route = respx.get("https://internal-hub:8080/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            env = {
                "SCION_HUB_URL": "https://internal-hub:8080",
                "SCION_AGENT_TOKEN": "agent-tkn",
            }
            with patch.dict(os.environ, env):
                client = ScionClient.from_agent_env()
                client.health()
                assert route.calls[0].request.headers["authorization"] == "Bearer agent-tkn"
                client.close()

    def test_from_agent_env_missing_url(self) -> None:
        with patch.dict(os.environ, {}, clear=True), pytest.raises(
            ValueError, match="SCION_HUB_URL"
        ):
            ScionClient.from_agent_env()

    def test_from_agent_env_missing_token(self) -> None:
        with patch.dict(os.environ, {"SCION_HUB_URL": "https://hub"}, clear=True), pytest.raises(
            ValueError, match="SCION_AGENT_TOKEN"
        ):
            ScionClient.from_agent_env()


class TestAsyncScionClient:
    @pytest.mark.asyncio
    async def test_health(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(
                    200,
                    json={"status": "ok", "version": "1.0.0"},
                )
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            health = await client.health()
            assert health.status == "ok"
            await client.close()

    @pytest.mark.asyncio
    async def test_async_context_manager(self) -> None:
        with respx.mock:
            respx.get(f"{BASE_URL}/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            async with AsyncScionClient(BASE_URL, token="test") as client:
                health = await client.health()
                assert health.status == "ok"

    @pytest.mark.asyncio
    async def test_from_agent_env(self) -> None:
        with respx.mock:
            route = respx.get("https://hub:9090/healthz").mock(
                return_value=httpx.Response(200, json={"status": "ok"})
            )
            env = {
                "SCION_HUB_URL": "https://hub:9090",
                "SCION_AGENT_TOKEN": "async-agent-tkn",
            }
            with patch.dict(os.environ, env):
                client = AsyncScionClient.from_agent_env()
                await client.health()
                assert (
                    route.calls[0].request.headers["authorization"] == "Bearer async-agent-tkn"
                )
                await client.close()

    @pytest.mark.asyncio
    async def test_from_agent_env_missing(self) -> None:
        with patch.dict(os.environ, {}, clear=True), pytest.raises(ValueError):
            AsyncScionClient.from_agent_env()

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

"""Asynchronous client for the Scion Hub API."""

from __future__ import annotations

import os

from scion._client import _resolve_token
from scion._transport import AsyncTransport
from scion.types.common import HealthResponse


class AsyncScionClient:
    """Asynchronous client for the Scion Hub API.

    Usage::

        client = AsyncScionClient("https://hub.example.com", token="my-token")
        health = await client.health()

        # Or with async context manager:
        async with AsyncScionClient("https://hub.example.com") as client:
            health = await client.health()

        # Auto-configure from agent environment:
        client = AsyncScionClient.from_agent_env()
    """

    def __init__(
        self,
        base_url: str,
        *,
        token: str | None = None,
        timeout: float = 30.0,
        max_retries: int = 3,
        headers: dict[str, str] | None = None,
    ) -> None:
        resolved_token = _resolve_token(token)
        self._transport = AsyncTransport(
            base_url,
            token=resolved_token,
            timeout=timeout,
            max_retries=max_retries,
            headers=headers,
        )

    @classmethod
    def from_agent_env(cls) -> AsyncScionClient:
        """Create a client configured from agent environment variables.

        Reads ``SCION_HUB_URL`` for the base URL and ``SCION_AGENT_TOKEN``
        for authentication. These are automatically set inside agent containers.

        Raises:
            ValueError: If required environment variables are not set.
        """
        base_url = os.environ.get("SCION_HUB_URL")
        if not base_url:
            raise ValueError("SCION_HUB_URL environment variable is not set")

        token = os.environ.get("SCION_AGENT_TOKEN")
        if not token:
            raise ValueError("SCION_AGENT_TOKEN environment variable is not set")

        return cls(base_url, token=token)

    async def health(self) -> HealthResponse:
        """Check API health.

        Returns:
            HealthResponse with status, version, and component checks.
        """
        resp = await self._transport.get("/healthz")
        return HealthResponse.model_validate(resp.json())

    # -- Service property stubs (to be filled by resource agents) --

    # @property
    # def agents(self) -> AsyncAgentService: ...

    # @property
    # def projects(self) -> AsyncProjectService: ...

    # @property
    # def secrets(self) -> AsyncSecretService: ...

    async def close(self) -> None:
        """Close the underlying HTTP transport."""
        await self._transport.close()

    async def __aenter__(self) -> AsyncScionClient:
        return self

    async def __aexit__(self, *args: object) -> None:
        await self.close()

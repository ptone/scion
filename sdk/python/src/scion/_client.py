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

"""Synchronous client for the Scion Hub API."""

from __future__ import annotations

import os
from pathlib import Path

from scion._transport import Transport
from scion.types.common import HealthResponse


def _resolve_token(
    token: str | None = None,
) -> str | None:
    """Resolve an API token from multiple sources.

    Resolution order:
    1. Explicit ``token`` parameter
    2. ``SCION_API_TOKEN`` environment variable
    3. ``SCION_DEV_TOKEN`` environment variable
    4. ``~/.scion/dev-token`` file
    """
    if token:
        return token

    env_token = os.environ.get("SCION_API_TOKEN")
    if env_token:
        return env_token

    dev_token = os.environ.get("SCION_DEV_TOKEN")
    if dev_token:
        return dev_token

    token_file = Path.home() / ".scion" / "dev-token"
    if token_file.is_file():
        content = token_file.read_text().strip()
        if content:
            return content

    return None


class ScionClient:
    """Synchronous client for the Scion Hub API.

    Usage::

        client = ScionClient("https://hub.example.com", token="my-token")
        health = client.health()

        # Or with context manager:
        with ScionClient("https://hub.example.com") as client:
            health = client.health()

        # Auto-configure from agent environment:
        client = ScionClient.from_agent_env()
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
        self._transport = Transport(
            base_url,
            token=resolved_token,
            timeout=timeout,
            max_retries=max_retries,
            headers=headers,
        )

    @classmethod
    def from_agent_env(cls) -> ScionClient:
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

    def health(self) -> HealthResponse:
        """Check API health.

        Returns:
            HealthResponse with status, version, and component checks.
        """
        resp = self._transport.get("/healthz")
        return HealthResponse.model_validate(resp.json())

    # -- Service property stubs (to be filled by resource agents) --

    # @property
    # def agents(self) -> AgentService: ...

    # @property
    # def projects(self) -> ProjectService: ...

    # @property
    # def secrets(self) -> SecretService: ...

    def close(self) -> None:
        """Close the underlying HTTP transport."""
        self._transport.close()

    def __enter__(self) -> ScionClient:
        return self

    def __exit__(self, *args: object) -> None:
        self.close()

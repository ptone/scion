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

"""Secrets resource for the Scion Python SDK.

Provides synchronous and asynchronous access to the Hub secrets API.
Note: Secret values are write-only and never returned by the API.

Mirrors Go implementation in ``pkg/hubclient/secrets.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING
from urllib.parse import quote

from scion.types.secrets import (
    ListSecretResponse,
    Secret,
    SetSecretRequest,
    SetSecretResponse,
)

if TYPE_CHECKING:
    from scion._transport import AsyncTransport, Transport


def _scope_params(
    scope: str | None = None,
    scope_id: str | None = None,
) -> dict[str, str] | None:
    """Build query-parameter dict from optional scope fields.

    Returns ``None`` when both values are absent so the transport
    skips the ``params`` kwarg entirely.
    """
    params: dict[str, str] = {}
    if scope is not None:
        params["scope"] = scope
    if scope_id is not None:
        params["scopeId"] = scope_id
    return params or None


class SecretsResource:
    """Synchronous resource for managing secrets.

    Obtained via ``ScionClient.secrets`` -- not instantiated directly.

    Usage::

        client = ScionClient(base_url, token=token)

        # List secrets
        response = client.secrets.list(scope="project", scope_id="proj-123")

        # Get a single secret's metadata
        secret = client.secrets.get("MY_KEY")

        # Create or update a secret
        result = client.secrets.set("MY_KEY", "super-secret-value")

        # Delete a secret
        client.secrets.delete("MY_KEY")
    """

    def __init__(self, transport: Transport) -> None:
        self._transport = transport

    def list(
        self,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
    ) -> ListSecretResponse:
        """List secret metadata for the specified scope.

        Secret values are never returned by the API.

        Args:
            scope: Scope filter -- ``"user"``, ``"project"``, or
                ``"runtime_broker"``.  Defaults to the server's default
                (typically ``"user"``).
            scope_id: ID of the scoped entity.  Required when *scope*
                is ``"project"`` or ``"runtime_broker"``.

        Returns:
            A :class:`ListSecretResponse` containing secret metadata.
        """
        params = _scope_params(scope, scope_id)
        resp = self._transport.get("/api/v1/secrets", params=params)
        return ListSecretResponse.model_validate(resp.json())

    def get(
        self,
        key: str,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
    ) -> Secret:
        """Get metadata for a specific secret by key.

        The secret value is never returned by the API.

        Args:
            key: The secret key to retrieve.
            scope: Scope filter (see :meth:`list`).
            scope_id: ID of the scoped entity (see :meth:`list`).

        Returns:
            A :class:`Secret` with metadata for the requested key.
        """
        params = _scope_params(scope, scope_id)
        path = f"/api/v1/secrets/{quote(key, safe='')}"
        resp = self._transport.get(path, params=params)
        return Secret.model_validate(resp.json())

    def set(
        self,
        key: str,
        value: str,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
        description: str | None = None,
        injection_mode: str | None = None,
        secret_type: str | None = None,
        target: str | None = None,
        allow_progeny: bool | None = None,
    ) -> SetSecretResponse:
        """Create or update a secret.

        Args:
            key: The secret key.
            value: The secret value (write-only -- never returned).
            scope: Scope type -- ``"user"``, ``"project"``, or
                ``"runtime_broker"``.  Defaults to the server's default.
            scope_id: ID of the scoped entity.  Required when *scope*
                is ``"project"`` or ``"runtime_broker"``.
            description: Optional human-readable description.
            injection_mode: ``"always"`` or ``"as_needed"``
                (default: ``"as_needed"``).
            secret_type: Secret type -- ``"environment"`` (default),
                ``"variable"``, or ``"file"``.
            target: Projection target (defaults to *key*).
            allow_progeny: Allow creator's progeny agents to access
                the secret (user scope only).

        Returns:
            A :class:`SetSecretResponse` with the updated metadata and
            a ``created`` flag indicating whether the secret was new.
        """
        req = SetSecretRequest(
            value=value,
            scope=scope,
            scope_id=scope_id,
            description=description,
            injection_mode=injection_mode,
            secret_type=secret_type,
            target=target,
            allow_progeny=allow_progeny,
        )
        path = f"/api/v1/secrets/{quote(key, safe='')}"
        resp = self._transport.put(path, json=req.model_dump_api())
        return SetSecretResponse.model_validate(resp.json())

    def delete(
        self,
        key: str,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
    ) -> None:
        """Delete a secret.

        Args:
            key: The secret key to delete.
            scope: Scope filter (see :meth:`list`).
            scope_id: ID of the scoped entity (see :meth:`list`).
        """
        params = _scope_params(scope, scope_id)
        path = f"/api/v1/secrets/{quote(key, safe='')}"
        self._transport.delete(path, params=params)


class AsyncSecretsResource:
    """Asynchronous resource for managing secrets.

    Obtained via ``AsyncScionClient.secrets`` -- not instantiated directly.

    Usage::

        client = AsyncScionClient(base_url, token=token)

        # List secrets
        response = await client.secrets.list(scope="project", scope_id="proj-123")

        # Get a single secret's metadata
        secret = await client.secrets.get("MY_KEY")

        # Create or update a secret
        result = await client.secrets.set("MY_KEY", "super-secret-value")

        # Delete a secret
        await client.secrets.delete("MY_KEY")
    """

    def __init__(self, transport: AsyncTransport) -> None:
        self._transport = transport

    async def list(
        self,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
    ) -> ListSecretResponse:
        """List secret metadata for the specified scope.

        Secret values are never returned by the API.

        Args:
            scope: Scope filter -- ``"user"``, ``"project"``, or
                ``"runtime_broker"``.  Defaults to the server's default
                (typically ``"user"``).
            scope_id: ID of the scoped entity.  Required when *scope*
                is ``"project"`` or ``"runtime_broker"``.

        Returns:
            A :class:`ListSecretResponse` containing secret metadata.
        """
        params = _scope_params(scope, scope_id)
        resp = await self._transport.get("/api/v1/secrets", params=params)
        return ListSecretResponse.model_validate(resp.json())

    async def get(
        self,
        key: str,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
    ) -> Secret:
        """Get metadata for a specific secret by key.

        The secret value is never returned by the API.

        Args:
            key: The secret key to retrieve.
            scope: Scope filter (see :meth:`list`).
            scope_id: ID of the scoped entity (see :meth:`list`).

        Returns:
            A :class:`Secret` with metadata for the requested key.
        """
        params = _scope_params(scope, scope_id)
        path = f"/api/v1/secrets/{quote(key, safe='')}"
        resp = await self._transport.get(path, params=params)
        return Secret.model_validate(resp.json())

    async def set(
        self,
        key: str,
        value: str,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
        description: str | None = None,
        injection_mode: str | None = None,
        secret_type: str | None = None,
        target: str | None = None,
        allow_progeny: bool | None = None,
    ) -> SetSecretResponse:
        """Create or update a secret.

        Args:
            key: The secret key.
            value: The secret value (write-only -- never returned).
            scope: Scope type -- ``"user"``, ``"project"``, or
                ``"runtime_broker"``.  Defaults to the server's default.
            scope_id: ID of the scoped entity.  Required when *scope*
                is ``"project"`` or ``"runtime_broker"``.
            description: Optional human-readable description.
            injection_mode: ``"always"`` or ``"as_needed"``
                (default: ``"as_needed"``).
            secret_type: Secret type -- ``"environment"`` (default),
                ``"variable"``, or ``"file"``.
            target: Projection target (defaults to *key*).
            allow_progeny: Allow creator's progeny agents to access
                the secret (user scope only).

        Returns:
            A :class:`SetSecretResponse` with the updated metadata and
            a ``created`` flag indicating whether the secret was new.
        """
        req = SetSecretRequest(
            value=value,
            scope=scope,
            scope_id=scope_id,
            description=description,
            injection_mode=injection_mode,
            secret_type=secret_type,
            target=target,
            allow_progeny=allow_progeny,
        )
        path = f"/api/v1/secrets/{quote(key, safe='')}"
        resp = await self._transport.put(path, json=req.model_dump_api())
        return SetSecretResponse.model_validate(resp.json())

    async def delete(
        self,
        key: str,
        *,
        scope: str | None = None,
        scope_id: str | None = None,
    ) -> None:
        """Delete a secret.

        Args:
            key: The secret key to delete.
            scope: Scope filter (see :meth:`list`).
            scope_id: ID of the scoped entity (see :meth:`list`).
        """
        params = _scope_params(scope, scope_id)
        path = f"/api/v1/secrets/{quote(key, safe='')}"
        await self._transport.delete(path, params=params)

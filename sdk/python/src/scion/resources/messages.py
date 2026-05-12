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

"""Messaging resource for the Scion Python SDK.

Provides ``MessagesResource`` (sync) and ``AsyncMessagesResource`` (async)
for interacting with the user's message inbox via the Hub API.
Mirrors the Go ``MessageService`` in ``pkg/hubclient/messages.go``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING
from urllib.parse import quote as url_quote

from scion._pagination import AsyncPage, SyncPage
from scion.types.messages import Message

if TYPE_CHECKING:
    from scion._transport import AsyncTransport, Transport


class MessagesResource:
    """Synchronous resource for message inbox operations.

    Provides methods for listing, retrieving, and managing read state
    of messages in the authenticated user's inbox.

    This class should not be instantiated directly — use
    :attr:`ScionClient.messages` instead.
    """

    def __init__(self, transport: Transport) -> None:
        self._transport = transport

    def list(
        self,
        *,
        limit: int | None = None,
        cursor: str | None = None,
        unread: bool | None = None,
        agent_id: str | None = None,
        project_id: str | None = None,
        type: str | None = None,
    ) -> SyncPage[Message]:
        """List messages in the user's inbox.

        Args:
            limit: Maximum number of messages to return per page.
            cursor: Pagination cursor for fetching subsequent pages.
            unread: If ``True``, only return unread messages.
            agent_id: Filter messages by agent ID.
            project_id: Filter messages by project ID.
            type: Filter messages by type (e.g. ``"instruction"``).

        Returns:
            A :class:`SyncPage` of :class:`Message` objects. Use
            :meth:`SyncPage.auto_paging_iter` to iterate across all pages.
        """
        params = self._build_params(
            limit=limit,
            cursor=cursor,
            unread=unread,
            agent_id=agent_id,
            project_id=project_id,
            type=type,
        )
        return self._fetch_page(params)

    def get(self, message_id: str) -> Message:
        """Retrieve a single message by ID.

        Args:
            message_id: The unique identifier of the message.

        Returns:
            The requested :class:`Message`.

        Raises:
            NotFoundError: If no message with the given ID exists.
        """
        resp = self._transport.get(f"/api/v1/messages/{url_quote(message_id, safe='')}")
        return Message.model_validate(resp.json())

    def mark_read(self, message_id: str) -> None:
        """Mark a single message as read.

        Args:
            message_id: The unique identifier of the message to mark as read.

        Raises:
            NotFoundError: If no message with the given ID exists.
        """
        self._transport.put(f"/api/v1/messages/{url_quote(message_id, safe='')}/read")

    def mark_all_read(self) -> None:
        """Mark all messages in the user's inbox as read."""
        self._transport.put("/api/v1/messages/read-all")

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    def _fetch_page(self, params: dict[str, str]) -> SyncPage[Message]:
        """Fetch a single page of messages from the API."""
        resp = self._transport.get("/api/v1/messages", params=params)
        data = resp.json()

        items_raw = data.get("items") or []
        items = [Message.model_validate(item) for item in items_raw]

        next_cursor = data.get("nextCursor") or None
        has_next = next_cursor is not None

        def _fetch_next(cursor: str) -> SyncPage[Message]:
            next_params = dict(params)
            next_params["cursor"] = cursor
            return self._fetch_page(next_params)

        return SyncPage(
            data=items,
            has_next=has_next,
            next_cursor=next_cursor,
            fetch_next=_fetch_next,
        )

    @staticmethod
    def _build_params(
        *,
        limit: int | None = None,
        cursor: str | None = None,
        unread: bool | None = None,
        agent_id: str | None = None,
        project_id: str | None = None,
        type: str | None = None,
    ) -> dict[str, str]:
        """Build query parameters dict, omitting ``None`` values."""
        params: dict[str, str] = {}
        if limit is not None:
            params["limit"] = str(limit)
        if cursor is not None:
            params["cursor"] = cursor
        if unread is not None and unread:
            params["unread"] = "true"
        if agent_id is not None:
            params["agent"] = agent_id
        if project_id is not None:
            params["project"] = project_id
        if type is not None:
            params["type"] = type
        return params


class AsyncMessagesResource:
    """Asynchronous resource for message inbox operations.

    Provides async methods for listing, retrieving, and managing read state
    of messages in the authenticated user's inbox.

    This class should not be instantiated directly — use
    :attr:`AsyncScionClient.messages` instead.
    """

    def __init__(self, transport: AsyncTransport) -> None:
        self._transport = transport

    async def list(
        self,
        *,
        limit: int | None = None,
        cursor: str | None = None,
        unread: bool | None = None,
        agent_id: str | None = None,
        project_id: str | None = None,
        type: str | None = None,
    ) -> AsyncPage[Message]:
        """List messages in the user's inbox.

        Args:
            limit: Maximum number of messages to return per page.
            cursor: Pagination cursor for fetching subsequent pages.
            unread: If ``True``, only return unread messages.
            agent_id: Filter messages by agent ID.
            project_id: Filter messages by project ID.
            type: Filter messages by type (e.g. ``"instruction"``).

        Returns:
            An :class:`AsyncPage` of :class:`Message` objects. Use
            :meth:`AsyncPage.auto_paging_iter` to iterate across all pages.
        """
        params = self._build_params(
            limit=limit,
            cursor=cursor,
            unread=unread,
            agent_id=agent_id,
            project_id=project_id,
            type=type,
        )
        return await self._fetch_page(params)

    async def get(self, message_id: str) -> Message:
        """Retrieve a single message by ID.

        Args:
            message_id: The unique identifier of the message.

        Returns:
            The requested :class:`Message`.

        Raises:
            NotFoundError: If no message with the given ID exists.
        """
        resp = await self._transport.get(
            f"/api/v1/messages/{url_quote(message_id, safe='')}"
        )
        return Message.model_validate(resp.json())

    async def mark_read(self, message_id: str) -> None:
        """Mark a single message as read.

        Args:
            message_id: The unique identifier of the message to mark as read.

        Raises:
            NotFoundError: If no message with the given ID exists.
        """
        await self._transport.put(
            f"/api/v1/messages/{url_quote(message_id, safe='')}/read"
        )

    async def mark_all_read(self) -> None:
        """Mark all messages in the user's inbox as read."""
        await self._transport.put("/api/v1/messages/read-all")

    # ------------------------------------------------------------------
    # Internal helpers
    # ------------------------------------------------------------------

    async def _fetch_page(self, params: dict[str, str]) -> AsyncPage[Message]:
        """Fetch a single page of messages from the API."""
        resp = await self._transport.get("/api/v1/messages", params=params)
        data = resp.json()

        items_raw = data.get("items") or []
        items = [Message.model_validate(item) for item in items_raw]

        next_cursor = data.get("nextCursor") or None
        has_next = next_cursor is not None

        async def _fetch_next(cursor: str) -> AsyncPage[Message]:
            next_params = dict(params)
            next_params["cursor"] = cursor
            return await self._fetch_page(next_params)

        return AsyncPage(
            data=items,
            has_next=has_next,
            next_cursor=next_cursor,
            fetch_next=_fetch_next,
        )

    @staticmethod
    def _build_params(
        *,
        limit: int | None = None,
        cursor: str | None = None,
        unread: bool | None = None,
        agent_id: str | None = None,
        project_id: str | None = None,
        type: str | None = None,
    ) -> dict[str, str]:
        """Build query parameters dict, omitting ``None`` values."""
        params: dict[str, str] = {}
        if limit is not None:
            params["limit"] = str(limit)
        if cursor is not None:
            params["cursor"] = cursor
        if unread is not None and unread:
            params["unread"] = "true"
        if agent_id is not None:
            params["agent"] = agent_id
        if project_id is not None:
            params["project"] = project_id
        if type is not None:
            params["type"] = type
        return params

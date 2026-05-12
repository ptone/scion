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

"""Tests for the MessagesResource and AsyncMessagesResource."""

import httpx
import pytest
import respx

from scion import AsyncScionClient, ScionClient
from scion._errors import NotFoundError
from scion.types.messages import Message

BASE_URL = "https://hub.example.com"


def _make_message_json(
    msg_id: str = "msg-1",
    *,
    sender: str = "user:alice",
    recipient: str = "agent:builder",
    msg: str = "hello",
    read: bool = False,
    agent_id: str = "agent-abc",
    project_id: str = "proj-1",
    msg_type: str = "instruction",
) -> dict:
    """Helper to build a message JSON payload."""
    return {
        "id": msg_id,
        "projectId": project_id,
        "sender": sender,
        "senderId": f"id-{sender}",
        "recipient": recipient,
        "recipientId": f"id-{recipient}",
        "msg": msg,
        "type": msg_type,
        "urgent": False,
        "broadcasted": False,
        "read": read,
        "agentId": agent_id,
        "createdAt": "2026-05-12T10:00:00Z",
    }


# ─── Sync MessagesResource ──────────────────────────────────────────────


class TestMessagesResourceList:
    """Tests for MessagesResource.list()."""

    def test_list_returns_messages(self) -> None:
        """List returns a page of Message objects."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "items": [_make_message_json("msg-1"), _make_message_json("msg-2")],
                        "nextCursor": None,
                    },
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.messages.list()
            assert len(page) == 2
            assert page.data[0].id == "msg-1"
            assert page.data[1].id == "msg-2"
            assert not page.has_next
            client.close()

    def test_list_empty(self) -> None:
        """List returns empty page when no messages exist."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(200, json={"items": []})
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.messages.list()
            assert len(page) == 0
            assert page.data == []
            assert not page.has_next
            client.close()

    def test_list_with_filters(self) -> None:
        """List passes filter params to the API."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(200, json={"items": []})
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.messages.list(
                limit=10,
                unread=True,
                agent_id="agent-xyz",
                project_id="proj-42",
                type="instruction",
            )
            request = route.calls[0].request
            assert "limit=10" in str(request.url)
            assert "unread=true" in str(request.url)
            assert "agent=agent-xyz" in str(request.url)
            assert "project=proj-42" in str(request.url)
            assert "type=instruction" in str(request.url)
            client.close()

    def test_list_unread_false_not_sent(self) -> None:
        """When unread=False, the unread param is not sent."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(200, json={"items": []})
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.messages.list(unread=False)
            request = route.calls[0].request
            assert "unread" not in str(request.url)
            client.close()

    def test_list_pagination(self) -> None:
        """List supports pagination via cursor."""
        with respx.mock:
            # Page 1
            respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "items": [_make_message_json("msg-1")],
                        "nextCursor": "cursor-2",
                    },
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            page1 = client.messages.list(limit=1)
            assert len(page1) == 1
            assert page1.has_next
            assert page1.next_cursor == "cursor-2"
            client.close()

    def test_list_auto_paging_iter(self) -> None:
        """auto_paging_iter fetches across multiple pages."""
        call_count = 0

        def _handle_request(request: httpx.Request) -> httpx.Response:
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                return httpx.Response(
                    200,
                    json={
                        "items": [_make_message_json("msg-1")],
                        "nextCursor": "cursor-2",
                    },
                )
            return httpx.Response(
                200,
                json={
                    "items": [_make_message_json("msg-2")],
                    "nextCursor": None,
                },
            )

        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages").mock(side_effect=_handle_request)
            client = ScionClient(BASE_URL, token="test-token")
            page = client.messages.list()
            all_msgs = list(page.auto_paging_iter())
            assert len(all_msgs) == 2
            assert all_msgs[0].id == "msg-1"
            assert all_msgs[1].id == "msg-2"
            client.close()

    def test_list_null_items(self) -> None:
        """List handles null items field gracefully."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(200, json={"items": None})
            )
            client = ScionClient(BASE_URL, token="test-token")
            page = client.messages.list()
            assert len(page) == 0
            assert page.data == []
            client.close()


class TestMessagesResourceGet:
    """Tests for MessagesResource.get()."""

    def test_get_message(self) -> None:
        """Get returns a single Message by ID."""
        msg_json = _make_message_json("msg-42", msg="test message")
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages/msg-42").mock(
                return_value=httpx.Response(200, json=msg_json)
            )
            client = ScionClient(BASE_URL, token="test-token")
            msg = client.messages.get("msg-42")
            assert msg.id == "msg-42"
            assert msg.msg == "test message"
            assert msg.sender == "user:alice"
            assert msg.agent_id == "agent-abc"
            assert msg.project_id == "proj-1"
            assert msg.type == "instruction"
            assert msg.read is False
            client.close()

    def test_get_message_not_found(self) -> None:
        """Get raises NotFoundError for a missing message."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages/nonexistent").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "Message not found"}},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            with pytest.raises(NotFoundError):
                client.messages.get("nonexistent")
            client.close()

    def test_get_message_url_encodes_id(self) -> None:
        """Get URL-encodes the message ID."""
        with respx.mock:
            route = respx.get(url__regex=r".*/api/v1/messages/.*").mock(
                return_value=httpx.Response(200, json=_make_message_json("msg/special"))
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.messages.get("msg/special")
            # The URL should have the encoded slash
            assert "msg%2Fspecial" in str(route.calls[0].request.url)
            client.close()


class TestMessagesResourceMarkRead:
    """Tests for MessagesResource.mark_read()."""

    def test_mark_read(self) -> None:
        """mark_read sends PUT to the correct endpoint."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/messages/msg-42/read").mock(
                return_value=httpx.Response(200, json={})
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.messages.mark_read("msg-42")
            assert route.called
            client.close()

    def test_mark_read_not_found(self) -> None:
        """mark_read raises NotFoundError for a missing message."""
        with respx.mock:
            respx.put(f"{BASE_URL}/api/v1/messages/nonexistent/read").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "Message not found"}},
                )
            )
            client = ScionClient(BASE_URL, token="test-token")
            with pytest.raises(NotFoundError):
                client.messages.mark_read("nonexistent")
            client.close()


class TestMessagesResourceMarkAllRead:
    """Tests for MessagesResource.mark_all_read()."""

    def test_mark_all_read(self) -> None:
        """mark_all_read sends PUT to the correct endpoint."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/messages/read-all").mock(
                return_value=httpx.Response(200, json={})
            )
            client = ScionClient(BASE_URL, token="test-token")
            client.messages.mark_all_read()
            assert route.called
            client.close()


class TestMessagesClientProperty:
    """Tests for the client.messages property."""

    def test_messages_property_returns_resource(self) -> None:
        """ScionClient.messages returns a MessagesResource."""
        client = ScionClient(BASE_URL, token="test-token")
        from scion.resources.messages import MessagesResource

        assert isinstance(client.messages, MessagesResource)
        client.close()

    def test_messages_property_caches_instance(self) -> None:
        """ScionClient.messages returns the same instance on repeated access."""
        client = ScionClient(BASE_URL, token="test-token")
        assert client.messages is client.messages
        client.close()


class TestMessageModel:
    """Tests for the Message Pydantic model."""

    def test_message_from_api_response(self) -> None:
        """Message parses all fields from a typical API response."""
        data = _make_message_json("msg-99", msg="important", read=True)
        msg = Message.model_validate(data)
        assert msg.id == "msg-99"
        assert msg.msg == "important"
        assert msg.read is True
        assert msg.project_id == "proj-1"
        assert msg.sender_id == "id-user:alice"
        assert msg.recipient == "agent:builder"
        assert msg.broadcasted is False
        assert msg.created_at is not None

    def test_message_defaults(self) -> None:
        """Message has reasonable defaults for missing fields."""
        msg = Message.model_validate({"id": "msg-1"})
        assert msg.id == "msg-1"
        assert msg.msg == ""
        assert msg.read is False
        assert msg.project_id is None
        assert msg.agent_id is None


# ─── Async MessagesResource ─────────────────────────────────────────────


class TestAsyncMessagesResourceList:
    """Tests for AsyncMessagesResource.list()."""

    @pytest.mark.asyncio
    async def test_list_returns_messages(self) -> None:
        """List returns a page of Message objects."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(
                    200,
                    json={
                        "items": [_make_message_json("msg-1")],
                        "nextCursor": None,
                    },
                )
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            page = await client.messages.list()
            assert len(page) == 1
            assert page.data[0].id == "msg-1"
            assert not page.has_next
            await client.close()

    @pytest.mark.asyncio
    async def test_list_with_filters(self) -> None:
        """List passes filter params to the API."""
        with respx.mock:
            route = respx.get(f"{BASE_URL}/api/v1/messages").mock(
                return_value=httpx.Response(200, json={"items": []})
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            await client.messages.list(unread=True, agent_id="agent-1", limit=5)
            request = route.calls[0].request
            assert "unread=true" in str(request.url)
            assert "agent=agent-1" in str(request.url)
            assert "limit=5" in str(request.url)
            await client.close()

    @pytest.mark.asyncio
    async def test_list_auto_paging_iter(self) -> None:
        """auto_paging_iter fetches across multiple pages."""
        call_count = 0

        def _handle_request(request: httpx.Request) -> httpx.Response:
            nonlocal call_count
            call_count += 1
            if call_count == 1:
                return httpx.Response(
                    200,
                    json={
                        "items": [_make_message_json("msg-a")],
                        "nextCursor": "c2",
                    },
                )
            return httpx.Response(
                200,
                json={
                    "items": [_make_message_json("msg-b")],
                    "nextCursor": None,
                },
            )

        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages").mock(side_effect=_handle_request)
            client = AsyncScionClient(BASE_URL, token="test-token")
            page = await client.messages.list()
            all_msgs = [item async for item in page.auto_paging_iter()]
            assert len(all_msgs) == 2
            assert all_msgs[0].id == "msg-a"
            assert all_msgs[1].id == "msg-b"
            await client.close()


class TestAsyncMessagesResourceGet:
    """Tests for AsyncMessagesResource.get()."""

    @pytest.mark.asyncio
    async def test_get_message(self) -> None:
        """Get returns a single Message by ID."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages/msg-1").mock(
                return_value=httpx.Response(200, json=_make_message_json("msg-1"))
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            msg = await client.messages.get("msg-1")
            assert msg.id == "msg-1"
            await client.close()

    @pytest.mark.asyncio
    async def test_get_not_found(self) -> None:
        """Get raises NotFoundError for a missing message."""
        with respx.mock:
            respx.get(f"{BASE_URL}/api/v1/messages/missing").mock(
                return_value=httpx.Response(
                    404,
                    json={"error": {"code": "not_found", "message": "not found"}},
                )
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            with pytest.raises(NotFoundError):
                await client.messages.get("missing")
            await client.close()


class TestAsyncMessagesResourceMarkRead:
    """Tests for AsyncMessagesResource.mark_read() and mark_all_read()."""

    @pytest.mark.asyncio
    async def test_mark_read(self) -> None:
        """mark_read sends PUT to the correct endpoint."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/messages/msg-1/read").mock(
                return_value=httpx.Response(200, json={})
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            await client.messages.mark_read("msg-1")
            assert route.called
            await client.close()

    @pytest.mark.asyncio
    async def test_mark_all_read(self) -> None:
        """mark_all_read sends PUT to the correct endpoint."""
        with respx.mock:
            route = respx.put(f"{BASE_URL}/api/v1/messages/read-all").mock(
                return_value=httpx.Response(200, json={})
            )
            client = AsyncScionClient(BASE_URL, token="test-token")
            await client.messages.mark_all_read()
            assert route.called
            await client.close()


class TestAsyncMessagesClientProperty:
    """Tests for the async client.messages property."""

    def test_messages_property_returns_resource(self) -> None:
        """AsyncScionClient.messages returns an AsyncMessagesResource."""
        client = AsyncScionClient(BASE_URL, token="test-token")
        from scion.resources.messages import AsyncMessagesResource

        assert isinstance(client.messages, AsyncMessagesResource)

    def test_messages_property_caches_instance(self) -> None:
        """AsyncScionClient.messages returns the same instance on repeated access."""
        client = AsyncScionClient(BASE_URL, token="test-token")
        assert client.messages is client.messages

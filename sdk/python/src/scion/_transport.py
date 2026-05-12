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

"""HTTP transport layer for the Scion Python SDK.

Provides sync and async transports wrapping httpx with:
- URL construction and JSON serialization
- Auth header injection
- Exponential backoff retry on 5xx / network errors
- Error response parsing
"""

from __future__ import annotations

import time as _time
from collections.abc import AsyncGenerator, Generator
from contextlib import asynccontextmanager, contextmanager
from typing import Any

import httpx

from scion._errors import ConnectionError, ScionError, parse_error_response

SDK_VERSION = "0.1.0"
DEFAULT_USER_AGENT = f"scion-python-sdk/{SDK_VERSION}"
DEFAULT_TIMEOUT = 30.0
DEFAULT_MAX_RETRIES = 3
DEFAULT_RETRY_BASE_DELAY = 0.5  # seconds


class Transport:
    """Synchronous HTTP transport for the Scion Hub API."""

    def __init__(
        self,
        base_url: str,
        *,
        token: str | None = None,
        timeout: float = DEFAULT_TIMEOUT,
        max_retries: int = DEFAULT_MAX_RETRIES,
        user_agent: str = DEFAULT_USER_AGENT,
        headers: dict[str, str] | None = None,
        http_client: httpx.Client | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._token = token
        self._max_retries = max_retries
        self._user_agent = user_agent
        self._extra_headers = headers or {}
        self._owns_client = http_client is None
        self._client = http_client or httpx.Client(timeout=timeout)

    def request(
        self,
        method: str,
        path: str,
        *,
        json: Any | None = None,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        """Execute an HTTP request with retry and error handling."""
        url = self._build_url(path)
        merged_headers = self._build_headers(headers)

        last_exc: Exception | None = None
        for attempt in range(self._max_retries + 1):
            try:
                resp = self._client.request(
                    method,
                    url,
                    json=json,
                    params=params,
                    headers=merged_headers,
                )
                if resp.status_code < 400:
                    return resp
                # Never retry 4xx
                if resp.status_code < 500:
                    raise parse_error_response(
                        resp.status_code,
                        resp.content,
                        headers=dict(resp.headers),
                    )
                # 5xx: retry if attempts remain
                if attempt < self._max_retries:
                    self._backoff(attempt)
                    continue
                raise parse_error_response(
                    resp.status_code,
                    resp.content,
                    headers=dict(resp.headers),
                )
            except ScionError:
                raise
            except httpx.TimeoutException as exc:
                last_exc = exc
                if attempt < self._max_retries:
                    self._backoff(attempt)
                    continue
            except httpx.ConnectError as exc:
                last_exc = exc
                if attempt < self._max_retries:
                    self._backoff(attempt)
                    continue
            except httpx.HTTPError as exc:
                last_exc = exc
                if attempt < self._max_retries:
                    self._backoff(attempt)
                    continue

        raise ConnectionError(
            f"Failed to connect after {self._max_retries + 1} attempts: {last_exc}",
        )

    def get(
        self,
        path: str,
        *,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return self.request("GET", path, params=params, headers=headers)

    def post(
        self,
        path: str,
        *,
        json: Any | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return self.request("POST", path, json=json, headers=headers)

    def put(
        self,
        path: str,
        *,
        json: Any | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return self.request("PUT", path, json=json, headers=headers)

    def patch(
        self,
        path: str,
        *,
        json: Any | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return self.request("PATCH", path, json=json, headers=headers)

    def delete(
        self,
        path: str,
        *,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return self.request("DELETE", path, params=params, headers=headers)

    @contextmanager
    def stream(
        self,
        method: str,
        path: str,
        *,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> Generator[httpx.Response, None, None]:
        """Open an HTTP streaming connection.

        Yields an httpx Response with streaming enabled. The caller must
        consume the response within the context manager block.

        Args:
            method: HTTP method (typically ``"GET"``).
            path: API path (e.g. ``/api/v1/agents/{id}/cloud-logs/stream``).
            params: Optional query parameters.
            headers: Optional extra headers (``Accept: text/event-stream``
                is typically added by the caller).
        """
        url = self._build_url(path)
        merged_headers = self._build_headers(headers)

        with self._client.stream(
            method,
            url,
            params=params,
            headers=merged_headers,
        ) as response:
            if response.status_code >= 400:
                # Read the body for error parsing
                response.read()
                raise parse_error_response(
                    response.status_code,
                    response.content,
                    headers=dict(response.headers),
                )
            yield response

    def close(self) -> None:
        if self._owns_client:
            self._client.close()

    def _build_url(self, path: str) -> str:
        if path.startswith("/"):
            return f"{self._base_url}{path}"
        return f"{self._base_url}/{path}"

    def _build_headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        headers: dict[str, str] = {
            "User-Agent": self._user_agent,
            "Accept": "application/json",
        }
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        headers.update(self._extra_headers)
        if extra:
            headers.update(extra)
        return headers

    @staticmethod
    def _backoff(attempt: int) -> None:
        delay = DEFAULT_RETRY_BASE_DELAY * (2**attempt)
        _time.sleep(delay)


class AsyncTransport:
    """Asynchronous HTTP transport for the Scion Hub API."""

    def __init__(
        self,
        base_url: str,
        *,
        token: str | None = None,
        timeout: float = DEFAULT_TIMEOUT,
        max_retries: int = DEFAULT_MAX_RETRIES,
        user_agent: str = DEFAULT_USER_AGENT,
        headers: dict[str, str] | None = None,
        http_client: httpx.AsyncClient | None = None,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._token = token
        self._max_retries = max_retries
        self._user_agent = user_agent
        self._extra_headers = headers or {}
        self._owns_client = http_client is None
        self._client = http_client or httpx.AsyncClient(timeout=timeout)

    async def request(
        self,
        method: str,
        path: str,
        *,
        json: Any | None = None,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        """Execute an async HTTP request with retry and error handling."""

        url = self._build_url(path)
        merged_headers = self._build_headers(headers)

        last_exc: Exception | None = None
        for attempt in range(self._max_retries + 1):
            try:
                resp = await self._client.request(
                    method,
                    url,
                    json=json,
                    params=params,
                    headers=merged_headers,
                )
                if resp.status_code < 400:
                    return resp
                if resp.status_code < 500:
                    raise parse_error_response(
                        resp.status_code,
                        resp.content,
                        headers=dict(resp.headers),
                    )
                if attempt < self._max_retries:
                    await self._async_backoff(attempt)
                    continue
                raise parse_error_response(
                    resp.status_code,
                    resp.content,
                    headers=dict(resp.headers),
                )
            except ScionError:
                raise
            except httpx.TimeoutException as exc:
                last_exc = exc
                if attempt < self._max_retries:
                    await self._async_backoff(attempt)
                    continue
            except httpx.ConnectError as exc:
                last_exc = exc
                if attempt < self._max_retries:
                    await self._async_backoff(attempt)
                    continue
            except httpx.HTTPError as exc:
                last_exc = exc
                if attempt < self._max_retries:
                    await self._async_backoff(attempt)
                    continue

        raise ConnectionError(
            f"Failed to connect after {self._max_retries + 1} attempts: {last_exc}",
        )

    async def get(
        self,
        path: str,
        *,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return await self.request("GET", path, params=params, headers=headers)

    async def post(
        self,
        path: str,
        *,
        json: Any | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return await self.request("POST", path, json=json, headers=headers)

    async def put(
        self,
        path: str,
        *,
        json: Any | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return await self.request("PUT", path, json=json, headers=headers)

    async def patch(
        self,
        path: str,
        *,
        json: Any | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return await self.request("PATCH", path, json=json, headers=headers)

    async def delete(
        self,
        path: str,
        *,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> httpx.Response:
        return await self.request("DELETE", path, params=params, headers=headers)

    @asynccontextmanager
    async def stream(
        self,
        method: str,
        path: str,
        *,
        params: dict[str, str] | None = None,
        headers: dict[str, str] | None = None,
    ) -> AsyncGenerator[httpx.Response, None]:
        """Open an async HTTP streaming connection.

        Yields an httpx Response with streaming enabled. The caller must
        consume the response within the async context manager block.

        Args:
            method: HTTP method (typically ``"GET"``).
            path: API path (e.g. ``/api/v1/agents/{id}/cloud-logs/stream``).
            params: Optional query parameters.
            headers: Optional extra headers (``Accept: text/event-stream``
                is typically added by the caller).
        """
        url = self._build_url(path)
        merged_headers = self._build_headers(headers)

        async with self._client.stream(
            method,
            url,
            params=params,
            headers=merged_headers,
        ) as response:
            if response.status_code >= 400:
                await response.aread()
                raise parse_error_response(
                    response.status_code,
                    response.content,
                    headers=dict(response.headers),
                )
            yield response

    async def close(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    def _build_url(self, path: str) -> str:
        if path.startswith("/"):
            return f"{self._base_url}{path}"
        return f"{self._base_url}/{path}"

    def _build_headers(self, extra: dict[str, str] | None = None) -> dict[str, str]:
        headers: dict[str, str] = {
            "User-Agent": self._user_agent,
            "Accept": "application/json",
        }
        if self._token:
            headers["Authorization"] = f"Bearer {self._token}"
        headers.update(self._extra_headers)
        if extra:
            headers.update(extra)
        return headers

    @staticmethod
    async def _async_backoff(attempt: int) -> None:
        import asyncio

        delay = DEFAULT_RETRY_BASE_DELAY * (2**attempt)
        await asyncio.sleep(delay)

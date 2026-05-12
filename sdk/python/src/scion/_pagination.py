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

"""Pagination helpers for the Scion Python SDK.

Provides ``SyncPage`` and ``AsyncPage`` wrappers that support
automatic iteration across paginated API responses.
"""

from __future__ import annotations

from typing import (
    TYPE_CHECKING,
    Any,
    Callable,
    Generic,
    TypeVar,
)

if TYPE_CHECKING:
    from collections.abc import AsyncIterator, Iterator

T = TypeVar("T")


class SyncPage(Generic[T]):
    """A single page of results from a paginated API endpoint.

    Attributes:
        data: The items on this page.
        has_next: Whether there are more pages available.
        next_cursor: The cursor value for fetching the next page.
    """

    def __init__(
        self,
        data: list[T],
        *,
        has_next: bool = False,
        next_cursor: str | None = None,
        fetch_next: Callable[[str], SyncPage[T]] | None = None,
    ) -> None:
        self.data = data
        self.has_next = has_next
        self.next_cursor = next_cursor
        self._fetch_next = fetch_next

    def __iter__(self) -> Iterator[T]:
        """Iterate over items on this page only."""
        return iter(self.data)

    def __len__(self) -> int:
        return len(self.data)

    def get_next_page(self) -> SyncPage[T]:
        """Fetch the next page of results.

        Raises:
            StopIteration: If there are no more pages.
        """
        if not self.has_next or self.next_cursor is None or self._fetch_next is None:
            raise StopIteration("No more pages")
        return self._fetch_next(self.next_cursor)

    def auto_paging_iter(self) -> Iterator[T]:
        """Iterate over all items across all pages automatically.

        Yields each item, fetching subsequent pages transparently.
        """
        page: SyncPage[T] | None = self
        while page is not None:
            yield from page.data
            if page.has_next and page.next_cursor and page._fetch_next:
                page = page._fetch_next(page.next_cursor)
            else:
                page = None


class AsyncPage(Generic[T]):
    """A single page of results from a paginated async API endpoint.

    Attributes:
        data: The items on this page.
        has_next: Whether there are more pages available.
        next_cursor: The cursor value for fetching the next page.
    """

    def __init__(
        self,
        data: list[T],
        *,
        has_next: bool = False,
        next_cursor: str | None = None,
        fetch_next: Callable[..., Any] | None = None,
    ) -> None:
        self.data = data
        self.has_next = has_next
        self.next_cursor = next_cursor
        self._fetch_next = fetch_next

    def __iter__(self) -> Iterator[T]:
        """Iterate over items on this page only (sync)."""
        return iter(self.data)

    def __len__(self) -> int:
        return len(self.data)

    async def get_next_page(self) -> AsyncPage[T]:
        """Fetch the next page of results asynchronously.

        Raises:
            StopAsyncIteration: If there are no more pages.
        """
        if not self.has_next or self.next_cursor is None or self._fetch_next is None:
            raise StopAsyncIteration("No more pages")
        return await self._fetch_next(self.next_cursor)

    async def auto_paging_iter(self) -> AsyncIterator[T]:
        """Iterate over all items across all pages automatically.

        Yields each item, fetching subsequent pages transparently.
        """
        page: AsyncPage[T] | None = self
        while page is not None:
            for item in page.data:
                yield item
            if page.has_next and page.next_cursor and page._fetch_next:
                page = await page._fetch_next(page.next_cursor)
            else:
                page = None

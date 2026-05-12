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

"""Tests for the pagination helpers."""

import pytest

from scion._pagination import AsyncPage, SyncPage


class TestSyncPage:
    def test_data_access(self) -> None:
        page = SyncPage(data=[1, 2, 3])
        assert page.data == [1, 2, 3]
        assert len(page) == 3

    def test_iter(self) -> None:
        page = SyncPage(data=["a", "b", "c"])
        assert list(page) == ["a", "b", "c"]

    def test_has_next_false(self) -> None:
        page = SyncPage(data=[1])
        assert not page.has_next
        assert page.next_cursor is None

    def test_get_next_page(self) -> None:
        page2 = SyncPage(data=[3, 4], has_next=False)
        page1 = SyncPage(
            data=[1, 2],
            has_next=True,
            next_cursor="cursor-2",
            fetch_next=lambda _: page2,
        )
        next_page = page1.get_next_page()
        assert next_page.data == [3, 4]

    def test_get_next_page_raises_when_no_more(self) -> None:
        page = SyncPage(data=[1])
        with pytest.raises(StopIteration):
            page.get_next_page()

    def test_auto_paging_iter_single_page(self) -> None:
        page = SyncPage(data=[1, 2, 3], has_next=False)
        items = list(page.auto_paging_iter())
        assert items == [1, 2, 3]

    def test_auto_paging_iter_multi_page(self) -> None:
        page3 = SyncPage(data=[5, 6], has_next=False)
        page2 = SyncPage(
            data=[3, 4],
            has_next=True,
            next_cursor="c3",
            fetch_next=lambda _: page3,
        )
        page1 = SyncPage(
            data=[1, 2],
            has_next=True,
            next_cursor="c2",
            fetch_next=lambda _: page2,
        )
        items = list(page1.auto_paging_iter())
        assert items == [1, 2, 3, 4, 5, 6]

    def test_auto_paging_iter_empty(self) -> None:
        page = SyncPage(data=[], has_next=False)
        items = list(page.auto_paging_iter())
        assert items == []


class TestAsyncPage:
    def test_data_access(self) -> None:
        page = AsyncPage(data=[1, 2, 3])
        assert page.data == [1, 2, 3]
        assert len(page) == 3

    def test_iter_sync(self) -> None:
        """AsyncPage supports sync iteration over current page data."""
        page = AsyncPage(data=["a", "b"])
        assert list(page) == ["a", "b"]

    @pytest.mark.asyncio
    async def test_get_next_page(self) -> None:
        page2 = AsyncPage(data=[3, 4], has_next=False)

        async def fetch(cursor: str) -> AsyncPage[int]:
            return page2

        page1 = AsyncPage(
            data=[1, 2],
            has_next=True,
            next_cursor="c2",
            fetch_next=fetch,
        )
        next_page = await page1.get_next_page()
        assert next_page.data == [3, 4]

    @pytest.mark.asyncio
    async def test_get_next_page_raises_when_no_more(self) -> None:
        page = AsyncPage(data=[1])
        with pytest.raises(StopAsyncIteration):
            await page.get_next_page()

    @pytest.mark.asyncio
    async def test_auto_paging_iter_single_page(self) -> None:
        page = AsyncPage(data=[1, 2, 3], has_next=False)
        items = [item async for item in page.auto_paging_iter()]
        assert items == [1, 2, 3]

    @pytest.mark.asyncio
    async def test_auto_paging_iter_multi_page(self) -> None:
        page3 = AsyncPage(data=[5, 6], has_next=False)

        async def fetch3(cursor: str) -> AsyncPage[int]:
            return page3

        page2 = AsyncPage(
            data=[3, 4],
            has_next=True,
            next_cursor="c3",
            fetch_next=fetch3,
        )

        async def fetch2(cursor: str) -> AsyncPage[int]:
            return page2

        page1 = AsyncPage(
            data=[1, 2],
            has_next=True,
            next_cursor="c2",
            fetch_next=fetch2,
        )
        items = [item async for item in page1.auto_paging_iter()]
        assert items == [1, 2, 3, 4, 5, 6]

    @pytest.mark.asyncio
    async def test_auto_paging_iter_empty(self) -> None:
        page = AsyncPage(data=[], has_next=False)
        items = [item async for item in page.auto_paging_iter()]
        assert items == []

import { describe, it, expect, vi } from 'vitest';
import { Page } from '../src/pagination.js';
import type { FetchNextPage } from '../src/pagination.js';

describe('Page', () => {
  describe('basic properties', () => {
    it('exposes data, hasNext, nextCursor, and totalCount', () => {
      const page = new Page([1, 2, 3], 'cursor-abc', undefined, 100);
      expect(page.data).toEqual([1, 2, 3]);
      expect(page.hasNext).toBe(true);
      expect(page.nextCursor).toBe('cursor-abc');
      expect(page.totalCount).toBe(100);
    });

    it('hasNext is false when nextCursor is undefined', () => {
      const page = new Page([1], undefined);
      expect(page.hasNext).toBe(false);
      expect(page.nextCursor).toBeUndefined();
    });

    it('hasNext is false when nextCursor is empty string', () => {
      const page = new Page([1], '');
      expect(page.hasNext).toBe(false);
    });

    it('totalCount is undefined when not provided', () => {
      const page = new Page([], undefined);
      expect(page.totalCount).toBeUndefined();
    });
  });

  describe('getNextPage', () => {
    it('returns null when there is no next page', async () => {
      const page = new Page([1], undefined);
      expect(await page.getNextPage()).toBeNull();
    });

    it('returns null when fetchNext is not provided', async () => {
      const page = new Page([1], 'cursor');
      expect(await page.getNextPage()).toBeNull();
    });

    it('calls fetchNext with the cursor and returns the next page', async () => {
      const page2 = new Page([4, 5, 6], undefined);
      const fetchNext = vi.fn<FetchNextPage<number>>().mockResolvedValue(page2);

      const page1 = new Page([1, 2, 3], 'cursor-2', fetchNext);
      const result = await page1.getNextPage();

      expect(fetchNext).toHaveBeenCalledWith('cursor-2');
      expect(result).toBe(page2);
    });
  });

  describe('AsyncIterable (for await...of)', () => {
    it('iterates over items on a single page', async () => {
      const page = new Page(['a', 'b', 'c'], undefined);
      const collected: string[] = [];

      for await (const item of page) {
        collected.push(item);
      }

      expect(collected).toEqual(['a', 'b', 'c']);
    });

    it('iterates across multiple pages transparently', async () => {
      // Set up a 3-page chain: [1,2] -> [3,4] -> [5]
      const page3 = new Page([5], undefined);
      const page2 = new Page([3, 4], 'c3', () => Promise.resolve(page3));
      const page1 = new Page([1, 2], 'c2', () => Promise.resolve(page2));

      const collected: number[] = [];
      for await (const item of page1) {
        collected.push(item);
      }

      expect(collected).toEqual([1, 2, 3, 4, 5]);
    });

    it('handles an empty first page gracefully', async () => {
      const page = new Page<number>([], undefined);
      const collected: number[] = [];

      for await (const item of page) {
        collected.push(item);
      }

      expect(collected).toEqual([]);
    });

    it('stops iteration when fetchNext returns null', async () => {
      const fetchNext = vi.fn<FetchNextPage<number>>().mockResolvedValue(null);

      const page = new Page([1, 2], 'cursor', fetchNext);
      const collected: number[] = [];

      for await (const item of page) {
        collected.push(item);
      }

      expect(collected).toEqual([1, 2]);
      expect(fetchNext).toHaveBeenCalledOnce();
    });

    it('calls fetchNext lazily (only when a page is exhausted)', async () => {
      const callOrder: string[] = [];

      const page2 = new Page([3], undefined);
      const fetchNext = vi.fn<FetchNextPage<number>>().mockImplementation(async () => {
        callOrder.push('fetch');
        return page2;
      });

      const page1 = new Page([1, 2], 'c2', fetchNext);

      for await (const item of page1) {
        callOrder.push(`item:${item}`);
      }

      // fetchNext should be called after items 1 and 2, before item 3
      expect(callOrder).toEqual(['item:1', 'item:2', 'fetch', 'item:3']);
    });
  });
});

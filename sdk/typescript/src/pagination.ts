/**
 * Pagination helpers for the Scion SDK.
 *
 * Provides the {@link Page} class that wraps a single page of results
 * and implements `AsyncIterable` for transparent auto-pagination via
 * `for await...of`.
 *
 * @packageDocumentation
 */

/**
 * Function signature used by {@link Page} to fetch the next page.
 *
 * @typeParam T - The type of items in the page.
 * @param cursor - The opaque cursor for the next page.
 * @returns A promise resolving to the next Page, or `null` if no more pages.
 */
export type FetchNextPage<T> = (cursor: string) => Promise<Page<T> | null>;

/**
 * A single page of paginated API results.
 *
 * Implements `AsyncIterable<T>` so callers can transparently iterate
 * across all pages using `for await...of`:
 *
 * @example
 * ```ts
 * const firstPage = await client.agents.list();
 * for await (const agent of firstPage) {
 *   console.log(agent.name);
 * }
 * ```
 *
 * @typeParam T - The type of items in this page.
 */
export class Page<T> implements AsyncIterable<T> {
  /** The items on this page. */
  readonly data: T[];

  /** Whether there is a subsequent page. */
  readonly hasNext: boolean;

  /** Opaque cursor to fetch the next page. `undefined` on the last page. */
  readonly nextCursor: string | undefined;

  /** Total number of matching items across all pages (if the server provided it). */
  readonly totalCount: number | undefined;

  /** @internal */
  private readonly fetchNext?: FetchNextPage<T>;

  /**
   * @param data - Items on this page.
   * @param nextCursor - Cursor for the next page, or `undefined` if this is the last page.
   * @param fetchNext - Callback to fetch the next page. Required for auto-pagination.
   * @param totalCount - Total number of matching items (optional).
   */
  constructor(
    data: T[],
    nextCursor: string | undefined,
    fetchNext?: FetchNextPage<T>,
    totalCount?: number,
  ) {
    this.data = data;
    this.nextCursor = nextCursor;
    this.hasNext = nextCursor !== undefined && nextCursor !== '';
    this.fetchNext = fetchNext;
    this.totalCount = totalCount;
  }

  /**
   * Fetch the next page of results.
   *
   * @returns The next Page, or `null` if there are no more pages.
   */
  async getNextPage(): Promise<Page<T> | null> {
    if (!this.hasNext || !this.nextCursor || !this.fetchNext) {
      return null;
    }
    return this.fetchNext(this.nextCursor);
  }

  /**
   * Async iterator that yields every item across this page and all
   * subsequent pages, fetching them lazily.
   */
  async *[Symbol.asyncIterator](): AsyncIterator<T> {
    // eslint-disable-next-line @typescript-eslint/no-this-alias
    let page: Page<T> | null = this;
    while (page) {
      for (const item of page.data) {
        yield item;
      }
      page = await page.getNextPage();
    }
  }
}

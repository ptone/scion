// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/** Options for cursor-based pagination. */
export interface PageParams {
  /** Maximum number of items to return per page. */
  limit?: number;
  /** Cursor for the next page (from a previous Page response). */
  cursor?: string;
}

/**
 * A page of results from a list endpoint.
 *
 * Implements `AsyncIterable` so callers can `for await` over all items
 * across every page:
 *
 * ```ts
 * for await (const agent of client.agents.list()) {
 *   console.log(agent.name);
 * }
 * ```
 */
export class Page<T> implements AsyncIterable<T> {
  /** Items in this page. */
  readonly data: T[];
  /** Cursor for the next page, or undefined if this is the last page. */
  readonly nextCursor?: string;
  /** Total count of matching items (if the server provided it). */
  readonly totalCount?: number;

  private readonly fetchPage: (cursor?: string) => Promise<Page<T>>;

  constructor(
    data: T[],
    nextCursor: string | undefined,
    totalCount: number | undefined,
    fetchPage: (cursor?: string) => Promise<Page<T>>,
  ) {
    this.data = data;
    this.nextCursor = nextCursor;
    this.totalCount = totalCount;
    this.fetchPage = fetchPage;
  }

  /** Whether there are more pages after this one. */
  get hasMore(): boolean {
    return this.nextCursor !== undefined && this.nextCursor !== "";
  }

  /** Fetch the next page. Returns undefined if there are no more pages. */
  async getNextPage(): Promise<Page<T> | undefined> {
    if (!this.hasMore) return undefined;
    return this.fetchPage(this.nextCursor);
  }

  /** Async iterator that yields every item across all pages. */
  async *[Symbol.asyncIterator](): AsyncIterator<T> {
    // eslint-disable-next-line @typescript-eslint/no-this-alias
    let page: Page<T> | undefined = this;
    while (page) {
      for (const item of page.data) {
        yield item;
      }
      page = await page.getNextPage();
    }
  }
}

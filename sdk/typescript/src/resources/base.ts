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

import type { Transport } from '../transport';
import type { Page, PageOptions, PageResult } from '../types/common';

/**
 * Base class for API resource modules.
 *
 * Provides shared access to the HTTP transport and common helper
 * methods for building paginated responses.
 */
export abstract class BaseResource {
  /** @internal */
  protected readonly transport: Transport;

  constructor(transport: Transport) {
    this.transport = transport;
  }

  /**
   * Build query parameters from a PageOptions object.
   *
   * @param params - Pagination and filter parameters.
   * @returns A flat record suitable for query string serialization.
   */
  protected buildPageQuery(params?: PageOptions): Record<string, string> {
    const query: Record<string, string> = {};
    if (params?.limit !== undefined) {
      query['limit'] = String(params.limit);
    }
    if (params?.cursor) {
      query['cursor'] = params.cursor;
    }
    return query;
  }

  /**
   * Wrap a raw API list response into a typed {@link Page}.
   *
   * @typeParam T - The item type.
   * @param items - Array of items from the API.
   * @param pageResult - Pagination metadata from the API.
   * @returns A typed page.
   */
  protected buildPage<T>(items: T[], pageResult: PageResult): Page<T> {
    return {
      data: items,
      page: {
        nextCursor: pageResult.nextCursor,
        totalCount: pageResult.totalCount,
      },
    };
  }
}

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

/** Options for paginated list requests. */
export interface PageOptions {
  /** Maximum results per page. */
  limit?: number;
  /** Cursor from a previous response to fetch the next page. */
  cursor?: string;
}

/** Pagination metadata returned with list responses. */
export interface PageResult {
  /** Cursor for the next page, empty if no more pages. */
  nextCursor?: string;
  /** Total count of items, if available. */
  totalCount?: number;
}

/** A paginated response wrapping an array of items. */
export interface Page<T> {
  /** The items in this page. */
  data: T[];
  /** Pagination metadata. */
  page: PageResult;
}

/** Structured API error from the Hub. */
export interface APIErrorResponse {
  /** Machine-readable error code. */
  code: string;
  /** Human-readable error message. */
  message: string;
  /** Additional context. */
  details?: Record<string, unknown>;
  /** Request tracking ID. */
  requestId?: string;
}

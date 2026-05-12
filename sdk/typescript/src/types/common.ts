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

/**
 * Paginated list result returned by all list endpoints.
 *
 * Mirrors the Go `store.ListResult[T]` generic.
 */
export interface Page<T> {
  /** The items in this page. */
  items: T[];
  /** Opaque cursor for fetching the next page. Empty when no more pages exist. */
  nextCursor?: string;
  /** Approximate total count of matching items (may be absent). */
  totalCount?: number;
}

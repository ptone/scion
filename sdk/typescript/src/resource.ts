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

import { Transport, RequestOptions } from "./transport";

/**
 * Page represents a paginated list response.
 */
export interface Page<T> {
  /** The items in this page. */
  data: T[];

  /** The scope of the listed items. */
  scope?: string;

  /** The scope ID of the listed items. */
  scopeId?: string;
}

/**
 * BaseResource is the base class for all API resource classes.
 * Provides access to the shared transport layer.
 */
export abstract class BaseResource {
  constructor(protected readonly transport: Transport) {}

  /** Builds query parameters from an object, omitting undefined/empty values. */
  protected buildQuery(
    params?: Record<string, string | undefined>,
  ): Record<string, string> | undefined {
    if (!params) return undefined;

    const query: Record<string, string> = {};
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== "") {
        query[key] = value;
      }
    }
    return Object.keys(query).length > 0 ? query : undefined;
  }
}

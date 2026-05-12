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

import { parseErrorResponse } from './errors.js';

/** Options for configuring the HTTP transport layer. */
export interface TransportOptions {
  /** Base URL of the Scion Hub API (e.g. "https://hub.scion.dev"). */
  baseUrl: string;
  /** Bearer token for authentication. */
  token?: string;
  /** Custom headers applied to every request. */
  headers?: Record<string, string>;
}

/**
 * Low-level HTTP transport for the Scion Hub API.
 *
 * Handles URL construction, authentication headers, JSON serialization,
 * and error response parsing. Resource classes use this to issue requests.
 */
export class Transport {
  private readonly baseUrl: string;
  private readonly defaultHeaders: Record<string, string>;

  constructor(options: TransportOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.defaultHeaders = {
      'User-Agent': 'scion-typescript-sdk/0.1.0',
      ...(options.token
        ? { Authorization: `Bearer ${options.token}` }
        : {}),
      ...(options.headers ?? {}),
    };
  }

  /**
   * Perform an HTTP GET request.
   *
   * @param path   - API path (e.g. "/api/v1/messages").
   * @param query  - Optional query parameters.
   * @returns The parsed JSON response body.
   */
  async get<T>(path: string, query?: Record<string, string>): Promise<T> {
    const url = this.buildUrl(path, query);
    const resp = await fetch(url, {
      method: 'GET',
      headers: this.defaultHeaders,
    });
    return this.handleResponse<T>(resp);
  }

  /**
   * Perform an HTTP POST request with an optional JSON body.
   *
   * @param path - API path.
   * @param body - Optional request body (will be JSON-serialized).
   * @returns The parsed JSON response body.
   */
  async post<T>(path: string, body?: unknown): Promise<T> {
    const headers: Record<string, string> = { ...this.defaultHeaders };
    let bodyStr: string | undefined;
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      bodyStr = JSON.stringify(body);
    }
    const resp = await fetch(this.buildUrl(path), {
      method: 'POST',
      headers,
      body: bodyStr,
    });
    return this.handleResponse<T>(resp);
  }

  /**
   * Perform an HTTP POST request that returns no body (e.g. 204 No Content).
   *
   * @param path - API path.
   * @param body - Optional request body.
   */
  async postEmpty(path: string, body?: unknown): Promise<void> {
    const headers: Record<string, string> = { ...this.defaultHeaders };
    let bodyStr: string | undefined;
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      bodyStr = JSON.stringify(body);
    }
    const resp = await fetch(this.buildUrl(path), {
      method: 'POST',
      headers,
      body: bodyStr,
    });
    if (!resp.ok) {
      throw await parseErrorResponse(resp);
    }
    // Drain the response body to avoid leaking connections.
    await resp.text();
  }

  // ---------------------------------------------------------------------------
  // Internal helpers
  // ---------------------------------------------------------------------------

  private buildUrl(path: string, query?: Record<string, string>): string {
    const url = new URL(path, this.baseUrl);
    if (query) {
      for (const [k, v] of Object.entries(query)) {
        if (v !== undefined && v !== '') {
          url.searchParams.set(k, v);
        }
      }
    }
    return url.toString();
  }

  private async handleResponse<T>(resp: Response): Promise<T> {
    if (!resp.ok) {
      throw await parseErrorResponse(resp);
    }
    if (resp.status === 204) {
      return undefined as unknown as T;
    }
    return (await resp.json()) as T;
  }
}

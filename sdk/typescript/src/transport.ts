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

import { ScionAPIError } from './errors';

/** HTTP method type. */
type HttpMethod = 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';

/** Options for configuring the HTTP transport. */
export interface TransportOptions {
  /** Base URL of the Scion Hub API (e.g. "https://hub.scion.dev"). */
  baseUrl: string;
  /** Bearer token for authentication. */
  token?: string;
  /** Custom fetch implementation (defaults to global fetch). */
  fetch?: typeof fetch;
  /** Default request timeout in milliseconds. */
  timeout?: number;
  /** Custom headers to include on every request. */
  headers?: Record<string, string>;
}

/**
 * Low-level HTTP transport for the Scion API.
 *
 * Handles JSON serialization, authentication headers, error parsing,
 * and provides typed request methods used by resource classes.
 */
export class Transport {
  private readonly baseUrl: string;
  private readonly token?: string;
  private readonly fetchFn: typeof fetch;
  private readonly timeout: number;
  private readonly defaultHeaders: Record<string, string>;

  constructor(options: TransportOptions) {
    // Strip trailing slash from base URL.
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.token = options.token;
    this.fetchFn = options.fetch ?? globalThis.fetch;
    this.timeout = options.timeout ?? 30_000;
    this.defaultHeaders = options.headers ?? {};
  }

  /**
   * Execute an HTTP request and return the parsed JSON response.
   *
   * @typeParam T - Expected response body type.
   * @param method - HTTP method.
   * @param path - API path (e.g. "/api/v1/projects").
   * @param query - Optional query parameters.
   * @param body - Optional JSON request body.
   * @returns Parsed response body.
   * @throws {ScionAPIError} If the API returns a non-2xx status.
   */
  async request<T>(
    method: HttpMethod,
    path: string,
    query?: Record<string, string>,
    body?: unknown,
  ): Promise<T> {
    const url = this.buildUrl(path, query);
    const headers: Record<string, string> = {
      ...this.defaultHeaders,
      'Content-Type': 'application/json',
      Accept: 'application/json',
    };

    if (this.token) {
      headers['Authorization'] = `Bearer ${this.token}`;
    }

    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), this.timeout);

    try {
      const response = await this.fetchFn(url, {
        method,
        headers,
        body: body !== undefined ? JSON.stringify(body) : undefined,
        signal: controller.signal,
      });

      if (!response.ok) {
        await this.handleError(response);
      }

      // 204 No Content — return undefined as T.
      if (response.status === 204) {
        return undefined as T;
      }

      return (await response.json()) as T;
    } finally {
      clearTimeout(timeoutId);
    }
  }

  /** Perform an HTTP GET. */
  async get<T>(path: string, query?: Record<string, string>): Promise<T> {
    return this.request<T>('GET', path, query);
  }

  /** Perform an HTTP POST. */
  async post<T>(path: string, body?: unknown, query?: Record<string, string>): Promise<T> {
    return this.request<T>('POST', path, query, body);
  }

  /** Perform an HTTP PUT. */
  async put<T>(path: string, body?: unknown, query?: Record<string, string>): Promise<T> {
    return this.request<T>('PUT', path, query, body);
  }

  /** Perform an HTTP PATCH. */
  async patch<T>(path: string, body?: unknown, query?: Record<string, string>): Promise<T> {
    return this.request<T>('PATCH', path, query, body);
  }

  /** Perform an HTTP DELETE. */
  async delete<T>(path: string, query?: Record<string, string>): Promise<T> {
    return this.request<T>('DELETE', path, query);
  }

  /** Build a full URL from a path and optional query parameters. */
  private buildUrl(path: string, query?: Record<string, string>): string {
    const url = new URL(path, this.baseUrl);
    if (query) {
      for (const [key, value] of Object.entries(query)) {
        if (value !== undefined && value !== '') {
          url.searchParams.append(key, value);
        }
      }
    }
    return url.toString();
  }

  /** Parse an error response and throw a ScionAPIError. */
  private async handleError(response: Response): Promise<never> {
    let code = 'unknown_error';
    let message = `API request failed with status ${response.status}`;
    let details: Record<string, unknown> | undefined;
    let requestId: string | undefined;

    try {
      const body = await response.json();
      if (body.code) code = body.code;
      if (body.message) message = body.message;
      if (body.details) details = body.details;
      if (body.requestId) requestId = body.requestId;
    } catch {
      // Response body is not JSON; use defaults.
    }

    throw new ScionAPIError(response.status, code, message, details, requestId);
  }
}

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

import { apiErrorFromStatus } from "./errors.js";

/** Function that returns headers for authentication. */
export type AuthProvider = () => Record<string, string>;

/** Options for the HTTP transport layer. */
export interface TransportOptions {
  /** Base URL of the Scion Hub API (e.g. "https://hub.example.com"). */
  baseUrl: string;
  /** Authentication provider that returns headers. */
  auth?: AuthProvider;
  /** Custom fetch implementation (defaults to global fetch). */
  fetch?: typeof globalThis.fetch;
  /** Default request timeout in milliseconds. */
  timeoutMs?: number;
}

/**
 * Low-level HTTP transport for the Scion API.
 *
 * Handles URL construction, authentication headers, JSON serialization,
 * and error response parsing. Resource classes delegate all HTTP work here.
 */
export class Transport {
  private readonly baseUrl: string;
  private readonly auth?: AuthProvider;
  private readonly fetchFn: typeof globalThis.fetch;
  private readonly timeoutMs: number;

  constructor(opts: TransportOptions) {
    // Strip trailing slash for consistent URL joining.
    this.baseUrl = opts.baseUrl.replace(/\/+$/, "");
    this.auth = opts.auth;
    this.fetchFn = opts.fetch ?? globalThis.fetch.bind(globalThis);
    this.timeoutMs = opts.timeoutMs ?? 30_000;
  }

  /**
   * Perform an HTTP request and return the parsed JSON body.
   * Throws an ApiError subclass for non-2xx responses.
   */
  async request<T>(
    method: string,
    path: string,
    opts?: {
      body?: unknown;
      query?: Record<string, string | string[] | undefined>;
      headers?: Record<string, string>;
      signal?: AbortSignal;
    },
  ): Promise<T> {
    const url = this.buildUrl(path, opts?.query);

    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Accept: "application/json",
      ...this.auth?.(),
      ...opts?.headers,
    };

    const controller = new AbortController();
    const externalSignal = opts?.signal;
    const timeout = setTimeout(() => controller.abort(), this.timeoutMs);

    // Forward external abort to our controller.
    if (externalSignal) {
      if (externalSignal.aborted) {
        controller.abort(externalSignal.reason);
      } else {
        externalSignal.addEventListener(
          "abort",
          () => controller.abort(externalSignal.reason),
          { once: true },
        );
      }
    }

    try {
      const response = await this.fetchFn(url, {
        method,
        headers,
        body: opts?.body !== undefined ? JSON.stringify(opts.body) : undefined,
        signal: controller.signal,
      });

      if (!response.ok) {
        const body = await response.text();
        let detail: string | undefined;
        try {
          const parsed = JSON.parse(body);
          detail = parsed.error ?? parsed.message ?? parsed.detail;
        } catch {
          // body is not JSON — use raw text
        }
        throw apiErrorFromStatus(response.status, body, detail);
      }

      // 204 No Content — return undefined cast to T.
      if (response.status === 204) {
        return undefined as T;
      }

      return (await response.json()) as T;
    } finally {
      clearTimeout(timeout);
    }
  }

  /** GET request. */
  async get<T>(
    path: string,
    query?: Record<string, string | string[] | undefined>,
    signal?: AbortSignal,
  ): Promise<T> {
    return this.request<T>("GET", path, { query, signal });
  }

  /** POST request with optional JSON body. */
  async post<T>(
    path: string,
    body?: unknown,
    signal?: AbortSignal,
  ): Promise<T> {
    return this.request<T>("POST", path, { body, signal });
  }

  /** PATCH request with JSON body. */
  async patch<T>(
    path: string,
    body: unknown,
    signal?: AbortSignal,
  ): Promise<T> {
    return this.request<T>("PATCH", path, { body, signal });
  }

  /** DELETE request. */
  async delete<T>(
    path: string,
    query?: Record<string, string | string[] | undefined>,
    signal?: AbortSignal,
  ): Promise<T> {
    return this.request<T>("DELETE", path, { query, signal });
  }

  /** Build a full URL from path and optional query parameters. */
  private buildUrl(
    path: string,
    query?: Record<string, string | string[] | undefined>,
  ): string {
    const url = new URL(this.baseUrl + path);
    if (query) {
      for (const [key, value] of Object.entries(query)) {
        if (value === undefined) continue;
        if (Array.isArray(value)) {
          for (const v of value) {
            url.searchParams.append(key, v);
          }
        } else {
          url.searchParams.set(key, value);
        }
      }
    }
    return url.toString();
  }
}

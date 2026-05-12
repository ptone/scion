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
 * Authenticator provides credentials for API requests.
 */
export interface Authenticator {
  /** Applies authentication headers to the request. */
  applyAuth(headers: Record<string, string>): void;
}

/** Bearer token authenticator. */
export class BearerAuth implements Authenticator {
  constructor(private readonly token: string) {}

  applyAuth(headers: Record<string, string>): void {
    headers["Authorization"] = `Bearer ${this.token}`;
  }
}

/** Agent token authenticator using X-Scion-Agent-Token header. */
export class AgentTokenAuth implements Authenticator {
  constructor(private readonly token: string) {}

  applyAuth(headers: Record<string, string>): void {
    headers["X-Scion-Agent-Token"] = this.token;
  }
}

/** ScionError represents an API error response. */
export class ScionError extends Error {
  constructor(
    message: string,
    public readonly status: number,
    public readonly body?: unknown,
  ) {
    super(message);
    this.name = "ScionError";
  }
}

/** RequestOptions for individual API calls. */
export interface RequestOptions {
  headers?: Record<string, string>;
  signal?: AbortSignal;
}

/**
 * Transport handles HTTP communication with the Scion Hub API.
 */
export class Transport {
  public auth?: Authenticator;

  constructor(
    private readonly baseURL: string,
    private readonly fetchFn: typeof fetch = globalThis.fetch,
  ) {}

  /**
   * Performs an HTTP request and returns the parsed response.
   */
  async request<T>(
    method: string,
    path: string,
    options?: {
      body?: unknown;
      query?: Record<string, string>;
      headers?: Record<string, string>;
      signal?: AbortSignal;
    },
  ): Promise<T> {
    const url = new URL(path, this.baseURL);
    if (options?.query) {
      for (const [key, value] of Object.entries(options.query)) {
        if (value !== undefined && value !== "") {
          url.searchParams.set(key, value);
        }
      }
    }

    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      ...options?.headers,
    };

    if (this.auth) {
      this.auth.applyAuth(headers);
    }

    const response = await this.fetchFn(url.toString(), {
      method,
      headers,
      body: options?.body ? JSON.stringify(options.body) : undefined,
      signal: options?.signal,
    });

    if (!response.ok) {
      let body: unknown;
      try {
        body = await response.json();
      } catch {
        body = await response.text().catch(() => undefined);
      }
      const message =
        typeof body === "object" && body !== null && "error" in body
          ? String((body as { error: unknown }).error)
          : `HTTP ${response.status}: ${response.statusText}`;
      throw new ScionError(message, response.status, body);
    }

    // 204 No Content
    if (response.status === 204) {
      return undefined as T;
    }

    return (await response.json()) as T;
  }

  /** Performs an HTTP GET request. */
  async get<T>(
    path: string,
    query?: Record<string, string>,
    options?: RequestOptions,
  ): Promise<T> {
    return this.request<T>("GET", path, {
      query,
      headers: options?.headers,
      signal: options?.signal,
    });
  }

  /** Performs an HTTP PUT request. */
  async put<T>(
    path: string,
    body: unknown,
    options?: RequestOptions,
  ): Promise<T> {
    return this.request<T>("PUT", path, {
      body,
      headers: options?.headers,
      signal: options?.signal,
    });
  }

  /** Performs an HTTP POST request. */
  async post<T>(
    path: string,
    body: unknown,
    options?: RequestOptions,
  ): Promise<T> {
    return this.request<T>("POST", path, {
      body,
      headers: options?.headers,
      signal: options?.signal,
    });
  }

  /** Performs an HTTP DELETE request. */
  async delete<T>(
    path: string,
    query?: Record<string, string>,
    options?: RequestOptions,
  ): Promise<T> {
    return this.request<T>("DELETE", path, {
      query,
      headers: options?.headers,
      signal: options?.signal,
    });
  }

  /** Performs an HTTP PATCH request. */
  async patch<T>(
    path: string,
    body: unknown,
    options?: RequestOptions,
  ): Promise<T> {
    return this.request<T>("PATCH", path, {
      body,
      headers: options?.headers,
      signal: options?.signal,
    });
  }
}

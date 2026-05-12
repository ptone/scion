/**
 * HTTP transport layer for the Scion SDK.
 *
 * Wraps native `fetch` with URL construction, JSON serialization,
 * authentication, timeout, and automatic retry with exponential backoff.
 *
 * @packageDocumentation
 */

import { ConnectionError, parseErrorResponse } from './errors.js';
import { SDK_VERSION } from './index.js';

/** Options accepted by the {@link Transport} constructor. */
export interface TransportOptions {
  /** Base URL of the Scion Hub API (e.g. `https://hub.example.com`). */
  baseUrl: string;
  /** Bearer token for authentication. */
  token?: string;
  /** Request timeout in milliseconds. Defaults to 30 000 (30 s). */
  timeout?: number;
  /** Extra headers merged into every request. */
  headers?: Record<string, string>;
}

/** Per-request options passed to {@link Transport.request} and {@link Transport.requestRaw}. */
export interface RequestOptions {
  /** JSON-serializable request body. */
  body?: unknown;
  /** URL query parameters. */
  query?: Record<string, string>;
  /** Extra headers for this request only. */
  headers?: Record<string, string>;
  /** Override the default timeout (ms) for this request. */
  timeout?: number;
  /** Provide an external AbortSignal to cancel the request. */
  signal?: AbortSignal;
}

/** Default timeout in milliseconds. */
const DEFAULT_TIMEOUT_MS = 30_000;

/** Maximum number of automatic retries on 5xx / network errors. */
const MAX_RETRIES = 3;

/** Base delay (ms) for exponential backoff. */
const BASE_BACKOFF_MS = 500;

/**
 * Low-level HTTP transport for the Scion Hub API.
 *
 * Handles URL construction, JSON body serialization, auth-header injection,
 * User-Agent, timeout via `AbortSignal.timeout`, and retry with exponential
 * backoff on 5xx / network errors.
 */
export class Transport {
  private readonly baseUrl: string;
  private readonly token?: string;
  private readonly timeout: number;
  private readonly defaultHeaders: Record<string, string>;

  constructor(options: TransportOptions) {
    this.baseUrl = options.baseUrl.replace(/\/+$/, '');
    this.token = options.token;
    this.timeout = options.timeout ?? DEFAULT_TIMEOUT_MS;
    this.defaultHeaders = {
      'User-Agent': `scion-typescript-sdk/${SDK_VERSION}`,
      ...options.headers,
    };
  }

  /**
   * Perform an HTTP request and return the parsed JSON response body.
   *
   * @typeParam T - Expected shape of the JSON response.
   * @param method - HTTP method (GET, POST, PUT, PATCH, DELETE).
   * @param path - URL path appended to the base URL (e.g. `/api/v1/agents`).
   * @param options - Optional body, query, headers, timeout, signal.
   * @returns The parsed JSON response.
   * @throws {ScionError} on 4xx/5xx responses.
   * @throws {ConnectionError} on network failures.
   */
  async request<T>(method: string, path: string, options?: RequestOptions): Promise<T> {
    const response = await this.requestRaw(method, path, options);

    // 204 No Content — return undefined (cast to T for void-returning callers)
    if (response.status === 204) {
      return undefined as T;
    }

    // Guard against empty bodies on other success statuses
    const text = await response.text();
    if (!text) {
      return undefined as T;
    }

    return JSON.parse(text) as T;
  }

  /**
   * Perform an HTTP request and return the raw {@link Response}.
   *
   * Useful for streaming responses or when the caller needs access to
   * headers / status before consuming the body.
   *
   * @param method - HTTP method.
   * @param path - URL path appended to the base URL.
   * @param options - Optional body, query, headers, timeout, signal.
   * @returns The raw fetch Response (body unconsumed).
   * @throws {ScionError} on 4xx/5xx responses.
   * @throws {ConnectionError} on network failures.
   */
  async requestRaw(method: string, path: string, options?: RequestOptions): Promise<Response> {
    const url = this.buildUrl(path, options?.query);
    const headers = this.buildHeaders(options?.headers, options?.body !== undefined);
    const timeoutMs = options?.timeout ?? this.timeout;

    const init: RequestInit = {
      method,
      headers,
      signal: this.buildSignal(timeoutMs, options?.signal),
    };

    if (options?.body !== undefined) {
      init.body = JSON.stringify(options.body);
    }

    let lastError: Error | undefined;

    for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
      try {
        const response = await fetch(url, init);

        // On 4xx, fail immediately (no retry)
        if (response.status >= 400 && response.status < 500) {
          throw await parseErrorResponse(response);
        }

        // On 5xx, retry if attempts remain
        if (response.status >= 500) {
          if (attempt < MAX_RETRIES) {
            lastError = await parseErrorResponse(response);
            await this.sleep(this.backoff(attempt));
            continue;
          }
          throw await parseErrorResponse(response);
        }

        return response;
      } catch (error) {
        // Re-throw SDK errors (already parsed above)
        if (error instanceof Error && error.name.endsWith('Error') && 'status' in error) {
          throw error;
        }

        // Network / abort errors — retry if attempts remain
        if (attempt < MAX_RETRIES) {
          lastError = error instanceof Error ? error : new Error(String(error));
          await this.sleep(this.backoff(attempt));
          continue;
        }

        throw new ConnectionError(
          `Request failed after ${MAX_RETRIES + 1} attempts: ${(error as Error).message}`,
          error instanceof Error ? error : undefined,
        );
      }
    }

    // Should not reach here, but satisfy the compiler
    throw (
      lastError ?? new ConnectionError('Request failed: unknown error')
    );
  }

  // ---------------------------------------------------------------------------
  // Internal helpers
  // ---------------------------------------------------------------------------

  private buildUrl(path: string, query?: Record<string, string>): string {
    const url = new URL(path, this.baseUrl);
    // new URL resolves relative to origin — ensure we keep the base path
    url.href = `${this.baseUrl}${path}`;
    if (query) {
      for (const [k, v] of Object.entries(query)) {
        url.searchParams.set(k, v);
      }
    }
    return url.toString();
  }

  private buildHeaders(extra?: Record<string, string>, hasBody?: boolean): Record<string, string> {
    const headers: Record<string, string> = { ...this.defaultHeaders };

    if (this.token) {
      headers['Authorization'] = `Bearer ${this.token}`;
    }

    if (hasBody) {
      headers['Content-Type'] = 'application/json';
    }

    if (extra) {
      Object.assign(headers, extra);
    }

    return headers;
  }

  private buildSignal(timeoutMs: number, external?: AbortSignal): AbortSignal {
    const timeoutSignal = AbortSignal.timeout(timeoutMs);
    if (!external) return timeoutSignal;

    // Combine external signal with timeout — abort on whichever fires first
    const controller = new AbortController();
    const onAbort = () => controller.abort();

    timeoutSignal.addEventListener('abort', onAbort, { once: true });
    external.addEventListener('abort', onAbort, { once: true });

    // If either is already aborted, propagate immediately
    if (timeoutSignal.aborted || external.aborted) {
      controller.abort();
    }

    return controller.signal;
  }

  private backoff(attempt: number): number {
    // Exponential backoff with jitter: base * 2^attempt + random jitter
    const delay = BASE_BACKOFF_MS * Math.pow(2, attempt);
    const jitter = Math.random() * BASE_BACKOFF_MS;
    return delay + jitter;
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
}

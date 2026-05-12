/**
 * Common types shared across the Scion SDK.
 *
 * @packageDocumentation
 */

/** Response from the `/healthz` endpoint. */
export interface HealthResponse {
  /** Overall health status (e.g. "ok"). */
  status: string;
  /** Server version string. */
  version: string;
  /** Scion platform version. */
  scionVersion: string;
  /** Server uptime as a human-readable duration. */
  uptime: string;
  /** Per-component health checks. */
  checks?: Record<string, string>;
  /** Composite web health (present in combo mode). */
  web?: unknown;
  /** Composite hub health (present in combo mode). */
  hub?: unknown;
  /** Composite broker health (present in combo mode). */
  broker?: unknown;
}

/** Cursor-based pagination parameters for list requests. */
export interface PageParams {
  /** Maximum number of results per page. */
  limit?: number;
  /** Opaque cursor from a previous response to fetch the next page. */
  cursor?: string;
}

/** Generic paginated response envelope. */
export interface PaginatedResponse<T> {
  /** The page of results. */
  items: T[];
  /** Opaque cursor for the next page. Empty/absent when no more pages. */
  nextCursor?: string;
  /** Total count of matching items (if the server provides it). */
  totalCount?: number;
}

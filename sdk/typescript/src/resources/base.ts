/**
 * Base class for API resource modules.
 *
 * Provides shared access to the HTTP transport and common helper methods
 * for building paginated responses and query parameters.
 *
 * @packageDocumentation
 */

import type { Transport } from '../transport.js';

/**
 * Abstract base for all Scion resource classes.
 *
 * Subclasses inherit the transport and common helpers for query building
 * and pagination.
 */
export abstract class BaseResource {
  /** @internal */
  protected readonly transport: Transport;

  constructor(transport: Transport) {
    this.transport = transport;
  }

  /**
   * Build query parameters from an object, omitting undefined/empty values.
   *
   * @param params - Key-value pairs to include as query parameters.
   * @returns A flat record suitable for query string serialization, or undefined if empty.
   */
  protected buildQuery(
    params?: Record<string, string | number | boolean | undefined>,
  ): Record<string, string> | undefined {
    if (!params) return undefined;

    const query: Record<string, string> = {};
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== '') {
        query[key] = String(value);
      }
    }
    return Object.keys(query).length > 0 ? query : undefined;
  }

  /**
   * Serialize a label map into a comma-separated string suitable for query params.
   *
   * @param labels - Key-value label pairs.
   * @returns A comma-separated string of "key=value" entries, or undefined if empty.
   */
  protected serializeLabels(labels?: Record<string, string>): string | undefined {
    if (!labels) return undefined;
    const entries = Object.entries(labels)
      .map(([k, v]) => `${k}=${v}`)
      .join(',');
    return entries || undefined;
  }
}

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

/** Structured error from the Scion Hub API. */
export class ScionAPIError extends Error {
  /** HTTP status code. */
  readonly statusCode: number;
  /** Machine-readable error code (e.g. "not_found"). */
  readonly code: string;
  /** Additional error context from the API. */
  readonly details: Record<string, unknown>;
  /** Request tracking ID. */
  readonly requestId: string;

  constructor(params: {
    statusCode: number;
    code: string;
    message: string;
    details?: Record<string, unknown>;
    requestId?: string;
  }) {
    super(params.message);
    this.name = 'ScionAPIError';
    this.statusCode = params.statusCode;
    this.code = params.code;
    this.details = params.details ?? {};
    this.requestId = params.requestId ?? '';
  }

  /** True if the error is a 404 Not Found. */
  isNotFound(): boolean {
    return this.statusCode === 404;
  }

  /** True if the error is a 401 Unauthorized. */
  isUnauthorized(): boolean {
    return this.statusCode === 401;
  }

  /** True if the error is a 403 Forbidden. */
  isForbidden(): boolean {
    return this.statusCode === 403;
  }

  /** True if the error is a 409 Conflict. */
  isConflict(): boolean {
    return this.statusCode === 409;
  }

  /** True if the error is a 429 Too Many Requests. */
  isRateLimited(): boolean {
    return this.statusCode === 429;
  }

  /** True if the error is a 5xx server error. */
  isServerError(): boolean {
    return this.statusCode >= 500 && this.statusCode < 600;
  }
}

/**
 * Parse an error response body into a ScionAPIError.
 *
 * Expects the API to return JSON with `code`, `message`, and optional `details`/`requestId` fields.
 * Falls back to status text when the body is not valid JSON.
 */
export async function parseErrorResponse(
  response: Response,
): Promise<ScionAPIError> {
  let code = 'unknown';
  let message = response.statusText || `HTTP ${response.status}`;
  let details: Record<string, unknown> = {};
  let requestId = '';

  try {
    const body = (await response.json()) as Record<string, unknown>;
    if (body && typeof body === 'object') {
      if (typeof body.code === 'string') code = body.code;
      if (typeof body.message === 'string') message = body.message;
      if (body.details && typeof body.details === 'object') {
        details = body.details as Record<string, unknown>;
      }
      if (typeof body.requestId === 'string') requestId = body.requestId;
    }
  } catch {
    // Body is not JSON – use defaults above.
  }

  return new ScionAPIError({
    statusCode: response.status,
    code,
    message,
    details,
    requestId,
  });
}

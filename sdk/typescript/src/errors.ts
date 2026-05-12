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

/** Error returned by the Scion Hub API. */
export class ScionAPIError extends Error {
  /** HTTP status code. */
  readonly statusCode: number;
  /** Machine-readable error code. */
  readonly code: string;
  /** Additional error context. */
  readonly details?: Record<string, unknown>;
  /** Request tracking ID. */
  readonly requestId?: string;

  constructor(
    statusCode: number,
    code: string,
    message: string,
    details?: Record<string, unknown>,
    requestId?: string,
  ) {
    super(message);
    this.name = 'ScionAPIError';
    this.statusCode = statusCode;
    this.code = code;
    this.details = details;
    this.requestId = requestId;
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

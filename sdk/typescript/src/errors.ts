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

/** Base error class for all Scion SDK errors. */
export class ScionError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ScionError";
  }
}

/** Error returned when the API responds with an HTTP error status. */
export class ApiError extends ScionError {
  /** HTTP status code. */
  readonly status: number;
  /** Raw response body, if available. */
  readonly body: string;
  /** Parsed error detail from the API, if available. */
  readonly detail?: string;

  constructor(status: number, body: string, detail?: string) {
    const msg = detail
      ? `API error ${status}: ${detail}`
      : `API error ${status}: ${body}`;
    super(msg);
    this.name = "ApiError";
    this.status = status;
    this.body = body;
    this.detail = detail;
  }
}

/** Error returned when authentication fails (401). */
export class AuthenticationError extends ApiError {
  constructor(body: string, detail?: string) {
    super(401, body, detail ?? "Authentication required");
    this.name = "AuthenticationError";
  }
}

/** Error returned when the caller lacks permission (403). */
export class AuthorizationError extends ApiError {
  constructor(body: string, detail?: string) {
    super(403, body, detail ?? "Permission denied");
    this.name = "AuthorizationError";
  }
}

/** Error returned when the requested resource is not found (404). */
export class NotFoundError extends ApiError {
  constructor(body: string, detail?: string) {
    super(404, body, detail ?? "Not found");
    this.name = "NotFoundError";
  }
}

/** Error returned when there is a conflict (409). */
export class ConflictError extends ApiError {
  constructor(body: string, detail?: string) {
    super(409, body, detail ?? "Conflict");
    this.name = "ConflictError";
  }
}

/** Error returned when the request is invalid (400). */
export class ValidationError extends ApiError {
  constructor(body: string, detail?: string) {
    super(400, body, detail ?? "Validation error");
    this.name = "ValidationError";
  }
}

/**
 * Creates the appropriate ApiError subclass for a given HTTP status code.
 */
export function apiErrorFromStatus(
  status: number,
  body: string,
  detail?: string,
): ApiError {
  switch (status) {
    case 400:
      return new ValidationError(body, detail);
    case 401:
      return new AuthenticationError(body, detail);
    case 403:
      return new AuthorizationError(body, detail);
    case 404:
      return new NotFoundError(body, detail);
    case 409:
      return new ConflictError(body, detail);
    default:
      return new ApiError(status, body, detail);
  }
}

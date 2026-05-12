/**
 * Error types for the Scion SDK.
 *
 * Provides a hierarchy of typed errors corresponding to HTTP status codes
 * and network conditions. The {@link parseErrorResponse} factory creates
 * the appropriate subclass from any non-OK fetch Response.
 *
 * @packageDocumentation
 */

/** Standard machine-readable error codes returned by the Scion API. */
export const ErrorCode = {
  INVALID_REQUEST: 'invalid_request',
  VALIDATION_ERROR: 'validation_error',
  UNAUTHORIZED: 'unauthorized',
  FORBIDDEN: 'forbidden',
  NOT_FOUND: 'not_found',
  CONFLICT: 'conflict',
  VERSION_CONFLICT: 'version_conflict',
  UNPROCESSABLE: 'unprocessable',
  RATE_LIMITED: 'rate_limited',
  INTERNAL_ERROR: 'internal_error',
  RUNTIME_ERROR: 'runtime_error',
  UNAVAILABLE: 'unavailable',
} as const;

/** Union type of all known error codes. */
export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

/** Shape of the JSON error envelope returned by the Scion API. */
interface ApiErrorBody {
  error?: {
    code?: string;
    message?: string;
    details?: Record<string, unknown>;
    requestId?: string;
  };
}

/**
 * Base error class for all Scion SDK errors.
 *
 * Contains structured fields parsed from the API response.
 */
export class ScionError extends Error {
  /** HTTP status code (0 for network/connection errors). */
  readonly status: number;
  /** Machine-readable error code. */
  readonly code: string;
  /** Optional request tracking ID from the server. */
  readonly requestId?: string;
  /** Optional additional context from the server. */
  readonly details?: Record<string, unknown>;

  constructor(
    message: string,
    status: number,
    code: string,
    requestId?: string,
    details?: Record<string, unknown>,
  ) {
    super(message);
    this.name = 'ScionError';
    this.status = status;
    this.code = code;
    this.requestId = requestId;
    this.details = details;
  }
}

/** Thrown when the server returns 401 Unauthorized. */
export class AuthenticationError extends ScionError {
  constructor(message: string, code: string, requestId?: string, details?: Record<string, unknown>) {
    super(message, 401, code, requestId, details);
    this.name = 'AuthenticationError';
  }
}

/** Thrown when the server returns 403 Forbidden. */
export class PermissionError extends ScionError {
  constructor(message: string, code: string, requestId?: string, details?: Record<string, unknown>) {
    super(message, 403, code, requestId, details);
    this.name = 'PermissionError';
  }
}

/** Thrown when the server returns 404 Not Found. */
export class NotFoundError extends ScionError {
  constructor(message: string, code: string, requestId?: string, details?: Record<string, unknown>) {
    super(message, 404, code, requestId, details);
    this.name = 'NotFoundError';
  }
}

/** Thrown when the server returns 409 Conflict. */
export class ConflictError extends ScionError {
  constructor(message: string, code: string, requestId?: string, details?: Record<string, unknown>) {
    super(message, 409, code, requestId, details);
    this.name = 'ConflictError';
  }
}

/** Thrown when the server returns 400 Bad Request. */
export class ValidationError extends ScionError {
  constructor(message: string, code: string, requestId?: string, details?: Record<string, unknown>) {
    super(message, 400, code, requestId, details);
    this.name = 'ValidationError';
  }
}

/** Thrown when the server returns 429 Too Many Requests. */
export class RateLimitError extends ScionError {
  /** Seconds to wait before retrying, if provided by the server. */
  readonly retryAfter?: number;

  constructor(
    message: string,
    code: string,
    requestId?: string,
    details?: Record<string, unknown>,
    retryAfter?: number,
  ) {
    super(message, 429, code, requestId, details);
    this.name = 'RateLimitError';
    this.retryAfter = retryAfter;
  }
}

/** Thrown when the server returns a 5xx status code. */
export class ServerError extends ScionError {
  constructor(
    message: string,
    status: number,
    code: string,
    requestId?: string,
    details?: Record<string, unknown>,
  ) {
    super(message, status, code, requestId, details);
    this.name = 'ServerError';
  }
}

/** Thrown on network-level failures (DNS, TCP, TLS). */
export class ConnectionError extends ScionError {
  /** The underlying system error, if available. */
  readonly cause?: Error;

  constructor(message: string, cause?: Error) {
    super(message, 0, 'connection_error');
    this.name = 'ConnectionError';
    this.cause = cause;
  }
}

/** Thrown on SSE / streaming errors. */
export class StreamError extends ScionError {
  constructor(message: string, cause?: Error) {
    super(message, 0, 'stream_error');
    this.name = 'StreamError';
    if (cause) {
      this.cause = cause;
    }
  }
}

/**
 * Maps an HTTP status code to a default error code string.
 */
function statusToCode(status: number): string {
  switch (status) {
    case 400:
      return ErrorCode.INVALID_REQUEST;
    case 401:
      return ErrorCode.UNAUTHORIZED;
    case 403:
      return ErrorCode.FORBIDDEN;
    case 404:
      return ErrorCode.NOT_FOUND;
    case 409:
      return ErrorCode.CONFLICT;
    case 422:
      return ErrorCode.UNPROCESSABLE;
    case 429:
      return ErrorCode.RATE_LIMITED;
    case 503:
      return ErrorCode.UNAVAILABLE;
    default:
      return status >= 500 ? ErrorCode.INTERNAL_ERROR : ErrorCode.INVALID_REQUEST;
  }
}

/**
 * Parses a non-OK {@link Response} into the appropriate {@link ScionError} subclass.
 *
 * Attempts to read the JSON error envelope from the response body.
 * Falls back to status text when the body cannot be parsed.
 *
 * @param response - A fetch Response with a non-2xx status code.
 * @returns A promise resolving to the appropriate ScionError subclass.
 */
export async function parseErrorResponse(response: Response): Promise<ScionError> {
  const status = response.status;
  let code = statusToCode(status);
  let message = response.statusText || `HTTP ${status}`;
  let requestId = response.headers.get('X-Request-ID') ?? undefined;
  let details: Record<string, unknown> | undefined;
  let retryAfter: number | undefined;

  // Try to parse JSON error body
  try {
    const body = (await response.json()) as ApiErrorBody;
    if (body.error) {
      if (body.error.code) code = body.error.code;
      if (body.error.message) message = body.error.message;
      if (body.error.requestId) requestId = body.error.requestId;
      if (body.error.details) details = body.error.details;
    }
  } catch {
    // Body was not JSON — use defaults from status
  }

  // Parse Retry-After for rate-limit responses
  if (status === 429) {
    const retryHeader = response.headers.get('Retry-After');
    if (retryHeader) {
      const parsed = parseInt(retryHeader, 10);
      if (!isNaN(parsed)) retryAfter = parsed;
    }
  }

  switch (status) {
    case 400:
      return new ValidationError(message, code, requestId, details);
    case 401:
      return new AuthenticationError(message, code, requestId, details);
    case 403:
      return new PermissionError(message, code, requestId, details);
    case 404:
      return new NotFoundError(message, code, requestId, details);
    case 409:
      return new ConflictError(message, code, requestId, details);
    case 429:
      return new RateLimitError(message, code, requestId, details, retryAfter);
    default:
      if (status >= 500) {
        return new ServerError(message, status, code, requestId, details);
      }
      return new ScionError(message, status, code, requestId, details);
  }
}

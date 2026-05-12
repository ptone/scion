import { describe, it, expect } from 'vitest';
import {
  ScionError,
  AuthenticationError,
  PermissionError,
  NotFoundError,
  ConflictError,
  ValidationError,
  RateLimitError,
  ServerError,
  ConnectionError,
  StreamError,
  ErrorCode,
  parseErrorResponse,
} from '../src/errors.js';

/**
 * Helper to create a mock Response with a JSON body.
 */
function mockResponse(
  status: number,
  body: unknown,
  headers?: Record<string, string>,
): Response {
  const headersInit = new Headers(headers);
  return new Response(JSON.stringify(body), {
    status,
    statusText: statusTextFor(status),
    headers: headersInit,
  });
}

function statusTextFor(status: number): string {
  const map: Record<number, string> = {
    400: 'Bad Request',
    401: 'Unauthorized',
    403: 'Forbidden',
    404: 'Not Found',
    409: 'Conflict',
    429: 'Too Many Requests',
    500: 'Internal Server Error',
    502: 'Bad Gateway',
    503: 'Service Unavailable',
  };
  return map[status] ?? '';
}

describe('Error classes', () => {
  it('ScionError has correct fields', () => {
    const err = new ScionError('test message', 400, 'invalid_request', 'req-1', { field: 'name' });
    expect(err.message).toBe('test message');
    expect(err.status).toBe(400);
    expect(err.code).toBe('invalid_request');
    expect(err.requestId).toBe('req-1');
    expect(err.details).toEqual({ field: 'name' });
    expect(err.name).toBe('ScionError');
    expect(err).toBeInstanceOf(Error);
  });

  it('AuthenticationError is a ScionError with status 401', () => {
    const err = new AuthenticationError('bad token', 'unauthorized');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(401);
    expect(err.name).toBe('AuthenticationError');
  });

  it('PermissionError is a ScionError with status 403', () => {
    const err = new PermissionError('access denied', 'forbidden');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(403);
    expect(err.name).toBe('PermissionError');
  });

  it('NotFoundError is a ScionError with status 404', () => {
    const err = new NotFoundError('not found', 'not_found');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(404);
    expect(err.name).toBe('NotFoundError');
  });

  it('ConflictError is a ScionError with status 409', () => {
    const err = new ConflictError('conflict', 'conflict');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(409);
    expect(err.name).toBe('ConflictError');
  });

  it('ValidationError is a ScionError with status 400', () => {
    const err = new ValidationError('bad input', 'validation_error');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(400);
    expect(err.name).toBe('ValidationError');
  });

  it('RateLimitError is a ScionError with status 429 and retryAfter', () => {
    const err = new RateLimitError('slow down', 'rate_limited', undefined, undefined, 60);
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(429);
    expect(err.name).toBe('RateLimitError');
    expect(err.retryAfter).toBe(60);
  });

  it('ServerError is a ScionError with a 5xx status', () => {
    const err = new ServerError('oops', 502, 'internal_error');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(502);
    expect(err.name).toBe('ServerError');
  });

  it('ConnectionError is a ScionError with status 0', () => {
    const cause = new Error('ECONNREFUSED');
    const err = new ConnectionError('network failure', cause);
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(0);
    expect(err.code).toBe('connection_error');
    expect(err.name).toBe('ConnectionError');
  });

  it('StreamError is a ScionError with status 0', () => {
    const err = new StreamError('stream broke');
    expect(err).toBeInstanceOf(ScionError);
    expect(err.status).toBe(0);
    expect(err.code).toBe('stream_error');
    expect(err.name).toBe('StreamError');
  });
});

describe('ErrorCode constants', () => {
  it('exposes standard API error codes', () => {
    expect(ErrorCode.NOT_FOUND).toBe('not_found');
    expect(ErrorCode.UNAUTHORIZED).toBe('unauthorized');
    expect(ErrorCode.RATE_LIMITED).toBe('rate_limited');
    expect(ErrorCode.INTERNAL_ERROR).toBe('internal_error');
  });
});

describe('parseErrorResponse', () => {
  it('parses a 400 with JSON error body into ValidationError', async () => {
    const resp = mockResponse(400, {
      error: {
        code: 'validation_error',
        message: 'name is required',
        details: { field: 'name' },
        requestId: 'req-123',
      },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(ValidationError);
    expect(err.status).toBe(400);
    expect(err.code).toBe('validation_error');
    expect(err.message).toBe('name is required');
    expect(err.requestId).toBe('req-123');
    expect(err.details).toEqual({ field: 'name' });
  });

  it('parses a 401 into AuthenticationError', async () => {
    const resp = mockResponse(401, {
      error: { code: 'unauthorized', message: 'invalid token' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(AuthenticationError);
    expect(err.status).toBe(401);
  });

  it('parses a 403 into PermissionError', async () => {
    const resp = mockResponse(403, {
      error: { code: 'forbidden', message: 'access denied' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(PermissionError);
  });

  it('parses a 404 into NotFoundError', async () => {
    const resp = mockResponse(404, {
      error: { code: 'not_found', message: 'agent not found' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(NotFoundError);
  });

  it('parses a 409 into ConflictError', async () => {
    const resp = mockResponse(409, {
      error: { code: 'conflict', message: 'already exists' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(ConflictError);
  });

  it('parses a 429 into RateLimitError with Retry-After', async () => {
    const resp = mockResponse(
      429,
      { error: { code: 'rate_limited', message: 'slow down' } },
      { 'Retry-After': '30' },
    );

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(RateLimitError);
    expect((err as RateLimitError).retryAfter).toBe(30);
  });

  it('parses a 500 into ServerError', async () => {
    const resp = mockResponse(500, {
      error: { code: 'internal_error', message: 'something broke' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(ServerError);
    expect(err.status).toBe(500);
  });

  it('parses a 502 into ServerError', async () => {
    const resp = mockResponse(502, {
      error: { code: 'internal_error', message: 'bad gateway' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(ServerError);
    expect(err.status).toBe(502);
  });

  it('falls back to status text when body is not JSON', async () => {
    const resp = new Response('plain text error', {
      status: 500,
      statusText: 'Internal Server Error',
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(ServerError);
    expect(err.message).toBe('Internal Server Error');
    expect(err.code).toBe('internal_error');
  });

  it('reads X-Request-ID header when body has no requestId', async () => {
    const resp = mockResponse(
      404,
      { error: { code: 'not_found', message: 'gone' } },
      { 'X-Request-ID': 'hdr-req-42' },
    );

    const err = await parseErrorResponse(resp);
    expect(err.requestId).toBe('hdr-req-42');
  });

  it('prefers body requestId over header', async () => {
    const resp = mockResponse(
      404,
      { error: { code: 'not_found', message: 'gone', requestId: 'body-req-99' } },
      { 'X-Request-ID': 'hdr-req-42' },
    );

    const err = await parseErrorResponse(resp);
    expect(err.requestId).toBe('body-req-99');
  });

  it('returns generic ScionError for unknown 4xx status', async () => {
    const resp = mockResponse(418, {
      error: { message: 'I am a teapot' },
    });

    const err = await parseErrorResponse(resp);
    expect(err).toBeInstanceOf(ScionError);
    expect(err).not.toBeInstanceOf(ValidationError);
    expect(err.status).toBe(418);
  });
});

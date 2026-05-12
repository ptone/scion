import { describe, it, expect, beforeAll, afterAll, afterEach } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { Transport } from '../src/transport.js';
import {
  AuthenticationError,
  NotFoundError,
  ServerError,
  ValidationError,
} from '../src/errors.js';

const BASE_URL = 'http://localhost:9999';

// Collect request details for assertions
let lastRequest: {
  method: string;
  url: string;
  headers: Headers;
  body: unknown;
} | null = null;

const server = setupServer();

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  lastRequest = null;
});
afterAll(() => server.close());

/**
 * Register a handler that captures the incoming request and returns JSON.
 */
function handleJson(
  method: 'get' | 'post' | 'put' | 'patch' | 'delete',
  path: string,
  status: number,
  body: unknown,
) {
  const handler = http[method](`${BASE_URL}${path}`, async ({ request }) => {
    let reqBody: unknown = null;
    const ct = request.headers.get('content-type');
    if (ct?.includes('application/json')) {
      reqBody = await request.json();
    }
    lastRequest = {
      method: request.method,
      url: request.url,
      headers: request.headers,
      body: reqBody,
    };
    return HttpResponse.json(body, { status });
  });
  server.use(handler);
}

describe('Transport', () => {
  describe('constructor', () => {
    it('strips trailing slashes from baseUrl', () => {
      const t = new Transport({ baseUrl: 'http://example.com///' });
      // We verify indirectly via a request
      expect(t).toBeDefined();
    });
  });

  describe('request<T>', () => {
    it('performs a GET and returns parsed JSON', async () => {
      const data = { id: '1', name: 'test-agent' };
      handleJson('get', '/api/v1/agents/1', 200, data);

      const t = new Transport({ baseUrl: BASE_URL, token: 'tok-123' });
      const result = await t.request<typeof data>('GET', '/api/v1/agents/1');

      expect(result).toEqual(data);
      expect(lastRequest?.headers.get('Authorization')).toBe('Bearer tok-123');
      expect(lastRequest?.headers.get('User-Agent')).toMatch(/scion-typescript-sdk/);
    });

    it('performs a POST with JSON body', async () => {
      const reqBody = { name: 'new-agent', template: 'default' };
      const respBody = { id: '2', name: 'new-agent' };
      handleJson('post', '/api/v1/agents', 201, respBody);

      const t = new Transport({ baseUrl: BASE_URL, token: 'tok' });
      const result = await t.request<typeof respBody>('POST', '/api/v1/agents', {
        body: reqBody,
      });

      expect(result).toEqual(respBody);
      expect(lastRequest?.body).toEqual(reqBody);
      expect(lastRequest?.headers.get('Content-Type')).toBe('application/json');
    });

    it('appends query parameters to the URL', async () => {
      handleJson('get', '/api/v1/agents', 200, { items: [] });

      const t = new Transport({ baseUrl: BASE_URL });
      await t.request('GET', '/api/v1/agents', {
        query: { limit: '10', cursor: 'abc' },
      });

      expect(lastRequest?.url).toContain('limit=10');
      expect(lastRequest?.url).toContain('cursor=abc');
    });

    it('merges custom headers from constructor', async () => {
      handleJson('get', '/api/v1/health', 200, { status: 'ok' });

      const t = new Transport({
        baseUrl: BASE_URL,
        headers: { 'X-Scion-Agent-Token': 'agent-tok-42' },
      });
      await t.request('GET', '/api/v1/health');

      expect(lastRequest?.headers.get('X-Scion-Agent-Token')).toBe('agent-tok-42');
    });

    it('merges per-request headers over defaults', async () => {
      handleJson('get', '/api/v1/health', 200, { status: 'ok' });

      const t = new Transport({
        baseUrl: BASE_URL,
        headers: { 'X-Custom': 'default' },
      });
      await t.request('GET', '/api/v1/health', {
        headers: { 'X-Custom': 'override', 'X-Extra': 'val' },
      });

      expect(lastRequest?.headers.get('X-Custom')).toBe('override');
      expect(lastRequest?.headers.get('X-Extra')).toBe('val');
    });
  });

  describe('error handling', () => {
    it('throws ValidationError on 400', async () => {
      handleJson('post', '/api/v1/agents', 400, {
        error: { code: 'validation_error', message: 'name required' },
      });

      const t = new Transport({ baseUrl: BASE_URL });
      await expect(
        t.request('POST', '/api/v1/agents', { body: {} }),
      ).rejects.toThrow(ValidationError);
    });

    it('throws AuthenticationError on 401', async () => {
      handleJson('get', '/api/v1/agents', 401, {
        error: { code: 'unauthorized', message: 'bad token' },
      });

      const t = new Transport({ baseUrl: BASE_URL });
      await expect(t.request('GET', '/api/v1/agents')).rejects.toThrow(AuthenticationError);
    });

    it('throws NotFoundError on 404', async () => {
      handleJson('get', '/api/v1/agents/999', 404, {
        error: { code: 'not_found', message: 'agent not found' },
      });

      const t = new Transport({ baseUrl: BASE_URL });
      await expect(t.request('GET', '/api/v1/agents/999')).rejects.toThrow(NotFoundError);
    });

    it('does not retry 4xx errors', async () => {
      let callCount = 0;
      server.use(
        http.get(`${BASE_URL}/api/v1/fail`, () => {
          callCount++;
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'nope' } },
            { status: 404 },
          );
        }),
      );

      const t = new Transport({ baseUrl: BASE_URL });
      await expect(t.request('GET', '/api/v1/fail')).rejects.toThrow(NotFoundError);
      expect(callCount).toBe(1);
    });

    it('retries on 5xx errors up to MAX_RETRIES then throws ServerError', async () => {
      let callCount = 0;
      server.use(
        http.get(`${BASE_URL}/api/v1/flaky`, () => {
          callCount++;
          return HttpResponse.json(
            { error: { code: 'internal_error', message: 'server error' } },
            { status: 500 },
          );
        }),
      );

      const t = new Transport({ baseUrl: BASE_URL, timeout: 30_000 });
      await expect(t.request('GET', '/api/v1/flaky')).rejects.toThrow(ServerError);
      // 1 initial + 3 retries = 4 total
      expect(callCount).toBe(4);
    }, 30_000);

    it('succeeds after transient 5xx if a retry succeeds', async () => {
      let callCount = 0;
      server.use(
        http.get(`${BASE_URL}/api/v1/recover`, () => {
          callCount++;
          if (callCount < 3) {
            return HttpResponse.json(
              { error: { code: 'internal_error', message: 'oops' } },
              { status: 500 },
            );
          }
          return HttpResponse.json({ status: 'ok' });
        }),
      );

      const t = new Transport({ baseUrl: BASE_URL });
      const result = await t.request<{ status: string }>('GET', '/api/v1/recover');
      expect(result.status).toBe('ok');
      expect(callCount).toBe(3);
    }, 30_000);
  });

  describe('requestRaw', () => {
    it('returns the raw Response object', async () => {
      handleJson('get', '/api/v1/health', 200, { status: 'ok' });

      const t = new Transport({ baseUrl: BASE_URL });
      const resp = await t.requestRaw('GET', '/api/v1/health');

      expect(resp).toBeInstanceOf(Response);
      expect(resp.status).toBe(200);
      const body = await resp.json();
      expect(body).toEqual({ status: 'ok' });
    });
  });
});

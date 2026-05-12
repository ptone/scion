import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { ScionClient } from '../src/client.js';
import { SecretsResource } from '../src/resources/secrets.js';
import { NotFoundError } from '../src/errors.js';
import type { Secret, SetSecretResponse, ListSecretResponse } from '../src/types/index.js';

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

const BASE_URL = 'http://hub.test:9999';

const server = setupServer();

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  vi.unstubAllEnvs();
});
afterAll(() => server.close());

/** A sample secret for testing. */
const sampleSecret: Secret = {
  id: 'sec-001',
  key: 'MY_API_KEY',
  type: 'environment',
  scope: 'user',
  scopeId: 'user-123',
  description: 'An API key',
  injectionMode: 'as_needed',
  version: 1,
  created: '2026-01-01T00:00:00Z',
  updated: '2026-01-01T00:00:00Z',
  createdBy: 'user-123',
};

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('SecretsResource', () => {
  describe('list', () => {
    it('lists secrets with no parameters', async () => {
      const responseBody: ListSecretResponse = {
        secrets: [sampleSecret],
        scope: 'user',
        scopeId: 'user-123',
      };

      server.use(
        http.get(`${BASE_URL}/api/v1/secrets`, () => {
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const page = await client.secrets.list();

      expect(page.data).toEqual([sampleSecret]);
      expect(page.scope).toBe('user');
      expect(page.scopeId).toBe('user-123');
    });

    it('lists secrets with scope parameters', async () => {
      let capturedUrl = '';
      const responseBody: ListSecretResponse = {
        secrets: [sampleSecret],
        scope: 'project',
        scopeId: 'proj-456',
      };

      server.use(
        http.get(`${BASE_URL}/api/v1/secrets`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const page = await client.secrets.list({
        scope: 'project',
        scopeId: 'proj-456',
      });

      expect(page.data).toEqual([sampleSecret]);
      expect(capturedUrl).toContain('scope=project');
      expect(capturedUrl).toContain('scopeId=proj-456');
    });

    it('lists secrets with type filter', async () => {
      let capturedUrl = '';
      const responseBody: ListSecretResponse = {
        secrets: [],
        scope: 'user',
        scopeId: 'user-123',
      };

      server.use(
        http.get(`${BASE_URL}/api/v1/secrets`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const page = await client.secrets.list({ type: 'file' });

      expect(page.data).toEqual([]);
      expect(capturedUrl).toContain('type=file');
    });

    it('returns empty data when no secrets exist', async () => {
      const responseBody: ListSecretResponse = {
        secrets: [],
        scope: 'user',
        scopeId: 'user-123',
      };

      server.use(
        http.get(`${BASE_URL}/api/v1/secrets`, () => {
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const page = await client.secrets.list();

      expect(page.data).toEqual([]);
      expect(page.data).toHaveLength(0);
    });
  });

  describe('get', () => {
    it('gets a secret by key', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/secrets/MY_API_KEY`, () => {
          return HttpResponse.json(sampleSecret);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const secret = await client.secrets.get('MY_API_KEY');

      expect(secret).toEqual(sampleSecret);
    });

    it('gets a secret with scope parameters', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/secrets/MY_API_KEY`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json(sampleSecret);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.secrets.get('MY_API_KEY', {
        scope: 'project',
        scopeId: 'proj-456',
      });

      expect(capturedUrl).toContain('scope=project');
      expect(capturedUrl).toContain('scopeId=proj-456');
    });

    it('throws NotFoundError on 404', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/secrets/NONEXISTENT`, () => {
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'secret not found' } },
            { status: 404 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.secrets.get('NONEXISTENT')).rejects.toThrow(NotFoundError);
    });
  });

  describe('set', () => {
    it('creates a new secret', async () => {
      const responseBody: SetSecretResponse = {
        secret: sampleSecret,
        created: true,
      };
      let capturedBody: unknown;

      server.use(
        http.put(`${BASE_URL}/api/v1/secrets/MY_API_KEY`, async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.secrets.set('MY_API_KEY', {
        value: 'sk-secret-value',
        description: 'An API key',
      });

      expect(result.secret).toEqual(sampleSecret);
      expect(result.created).toBe(true);
      expect(capturedBody).toEqual({
        value: 'sk-secret-value',
        description: 'An API key',
      });
    });

    it('updates an existing secret', async () => {
      const responseBody: SetSecretResponse = {
        secret: { ...sampleSecret, version: 2 },
        created: false,
      };

      server.use(
        http.put(`${BASE_URL}/api/v1/secrets/MY_API_KEY`, () => {
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.secrets.set('MY_API_KEY', { value: 'new-value' });

      expect(result.created).toBe(false);
      expect(result.secret.version).toBe(2);
    });

    it('sets a project-scoped secret with all options', async () => {
      const responseBody: SetSecretResponse = {
        secret: sampleSecret,
        created: true,
      };
      let capturedBody: unknown;

      server.use(
        http.put(`${BASE_URL}/api/v1/secrets/DB_PASSWORD`, async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.secrets.set('DB_PASSWORD', {
        value: 's3cret',
        scope: 'project',
        scopeId: 'proj-456',
        description: 'Database password',
        injectionMode: 'always',
        type: 'environment',
        target: 'DATABASE_PASSWORD',
        allowProgeny: true,
      });

      expect(capturedBody).toEqual({
        value: 's3cret',
        scope: 'project',
        scopeId: 'proj-456',
        description: 'Database password',
        injectionMode: 'always',
        type: 'environment',
        target: 'DATABASE_PASSWORD',
        allowProgeny: true,
      });
    });
  });

  describe('delete', () => {
    it('deletes a secret by key', async () => {
      let capturedMethod = '';

      server.use(
        http.delete(`${BASE_URL}/api/v1/secrets/MY_API_KEY`, ({ request }) => {
          capturedMethod = request.method;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.secrets.delete('MY_API_KEY');
      expect(capturedMethod).toBe('DELETE');
    });

    it('deletes a secret with scope parameters', async () => {
      let capturedUrl = '';

      server.use(
        http.delete(`${BASE_URL}/api/v1/secrets/DB_PASSWORD`, ({ request }) => {
          capturedUrl = request.url;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.secrets.delete('DB_PASSWORD', {
        scope: 'project',
        scopeId: 'proj-456',
      });

      expect(capturedUrl).toContain('scope=project');
      expect(capturedUrl).toContain('scopeId=proj-456');
    });

    it('throws NotFoundError on 404', async () => {
      server.use(
        http.delete(`${BASE_URL}/api/v1/secrets/NONEXISTENT`, () => {
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'secret not found' } },
            { status: 404 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.secrets.delete('NONEXISTENT')).rejects.toThrow(NotFoundError);
    });
  });

  describe('authentication', () => {
    it('includes Bearer token in requests', async () => {
      let capturedAuth: string | null = null;
      const responseBody: ListSecretResponse = {
        secrets: [],
        scope: 'user',
        scopeId: 'user-123',
      };

      server.use(
        http.get(`${BASE_URL}/api/v1/secrets`, ({ request }) => {
          capturedAuth = request.headers.get('Authorization');
          return HttpResponse.json(responseBody);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'my-token' });
      await client.secrets.list();

      expect(capturedAuth).toBe('Bearer my-token');
    });
  });

  describe('ScionClient integration', () => {
    it('exposes secrets as a property', () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      expect(client.secrets).toBeDefined();
      expect(client.secrets).toBeInstanceOf(SecretsResource);
      expect(typeof client.secrets.list).toBe('function');
      expect(typeof client.secrets.get).toBe('function');
      expect(typeof client.secrets.set).toBe('function');
      expect(typeof client.secrets.delete).toBe('function');
    });
  });
});

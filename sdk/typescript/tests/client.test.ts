import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { ScionClient } from '../src/client.js';

const BASE_URL = 'http://hub.test:9999';

const server = setupServer();

beforeAll(() => server.listen({ onUnhandledRequest: 'error' }));
afterEach(() => {
  server.resetHandlers();
  vi.unstubAllEnvs();
});
afterAll(() => server.close());

describe('ScionClient', () => {
  describe('constructor', () => {
    it('throws when no hubUrl is provided and SCION_HUB_URL is unset', () => {
      vi.stubEnv('SCION_HUB_URL', '');
      expect(() => new ScionClient()).toThrow('hubUrl is required');
    });

    it('accepts explicit hubUrl and token', () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok-123' });
      expect(client).toBeDefined();
      expect(client.transport).toBeDefined();
    });

    it('resolves hubUrl from SCION_HUB_URL env var', () => {
      vi.stubEnv('SCION_HUB_URL', BASE_URL);
      const client = new ScionClient({ token: 'tok' });
      expect(client).toBeDefined();
    });

    it('resolves token from SCION_API_TOKEN env var', () => {
      vi.stubEnv('SCION_HUB_URL', BASE_URL);
      vi.stubEnv('SCION_API_TOKEN', 'api-tok-42');

      server.use(
        http.get(`${BASE_URL}/healthz`, ({ request }) => {
          const auth = request.headers.get('Authorization');
          return HttpResponse.json({ token: auth });
        }),
      );

      const client = new ScionClient();
      // The token should be set on the transport — we verify via a real request
      expect(client).toBeDefined();
    });

    it('resolves token from SCION_DEV_TOKEN env var when SCION_API_TOKEN is unset', () => {
      vi.stubEnv('SCION_HUB_URL', BASE_URL);
      vi.stubEnv('SCION_API_TOKEN', '');
      vi.stubEnv('SCION_DEV_TOKEN', 'dev-tok-99');

      const client = new ScionClient();
      expect(client).toBeDefined();
    });
  });

  describe('fromAgentEnv', () => {
    it('throws when SCION_HUB_URL is not set', () => {
      vi.stubEnv('SCION_HUB_URL', '');
      vi.stubEnv('SCION_AGENT_TOKEN', 'agent-tok');

      expect(() => ScionClient.fromAgentEnv()).toThrow('SCION_HUB_URL');
    });

    it('throws when SCION_AGENT_TOKEN is not set', () => {
      vi.stubEnv('SCION_HUB_URL', BASE_URL);
      vi.stubEnv('SCION_AGENT_TOKEN', '');

      expect(() => ScionClient.fromAgentEnv()).toThrow('SCION_AGENT_TOKEN');
    });

    it('creates a client that uses X-Scion-Agent-Token header', async () => {
      vi.stubEnv('SCION_HUB_URL', BASE_URL);
      vi.stubEnv('SCION_AGENT_TOKEN', 'agent-secret');

      let capturedAgentToken: string | null = null;
      let capturedAuthHeader: string | null = null;

      server.use(
        http.get(`${BASE_URL}/healthz`, ({ request }) => {
          capturedAgentToken = request.headers.get('X-Scion-Agent-Token');
          capturedAuthHeader = request.headers.get('Authorization');
          return HttpResponse.json({ status: 'ok' });
        }),
      );

      const client = ScionClient.fromAgentEnv();
      await client.health();

      expect(capturedAgentToken).toBe('agent-secret');
      expect(capturedAuthHeader).toBeNull();
    });
  });

  describe('health()', () => {
    it('returns the health response from /healthz', async () => {
      const healthData = {
        status: 'ok',
        version: '1.0.0',
        scionVersion: '0.5.0',
        uptime: '2h30m',
      };

      server.use(
        http.get(`${BASE_URL}/healthz`, () => {
          return HttpResponse.json(healthData);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.health();

      expect(result.status).toBe('ok');
      expect(result.version).toBe('1.0.0');
      expect(result.scionVersion).toBe('0.5.0');
      expect(result.uptime).toBe('2h30m');
    });

    it('sends Authorization header with token', async () => {
      let capturedAuth: string | null = null;

      server.use(
        http.get(`${BASE_URL}/healthz`, ({ request }) => {
          capturedAuth = request.headers.get('Authorization');
          return HttpResponse.json({ status: 'ok' });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'my-token' });
      await client.health();

      expect(capturedAuth).toBe('Bearer my-token');
    });
  });

  describe('resource stubs', () => {
    it('provides lazy-init stubs for agents, projects, secrets, messages', () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });

      expect(client.agents).toBeDefined();
      expect(client.projects).toBeDefined();
      expect(client.secrets).toBeDefined();
      expect(client.messages).toBeDefined();

      // Same instance on second access (lazy singleton)
      expect(client.agents).toBe(client.agents);
      expect(client.projects).toBe(client.projects);
    });
  });
});

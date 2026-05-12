import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { ScionClient } from '../src/client.js';
import { AgentsResource } from '../src/resources/agents.js';
import { NotFoundError, AuthenticationError, ValidationError } from '../src/errors.js';
import type { Agent, CreateAgentRequest, StructuredMessage } from '../src/types/index.js';

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

/** Minimal agent fixture. */
function makeAgent(overrides: Partial<Agent> = {}): Agent {
  return {
    id: 'agent-001',
    slug: 'code-reviewer',
    containerId: 'ctr-abc',
    name: 'code-reviewer',
    status: 'running',
    phase: 'running',
    created: '2026-05-12T00:00:00Z',
    updated: '2026-05-12T00:00:00Z',
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('AgentsResource', () => {
  describe('create', () => {
    it('posts to /api/v1/agents with the correct body', async () => {
      const agent = makeAgent();
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/agents`, async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json({ agent, warnings: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const params: CreateAgentRequest = {
        name: 'code-reviewer',
        projectId: 'proj-123',
        template: 'claude',
        task: 'Review PR #42',
      };
      const result = await client.agents.create(params);

      expect(capturedBody).toEqual(params);
      expect(result.agent.id).toBe('agent-001');
    });

    it('posts to project-scoped path when projectId is set on client', async () => {
      const agent = makeAgent();
      let capturedUrl = '';

      server.use(
        http.post(`${BASE_URL}/api/v1/projects/:projectId/agents`, ({ request }) => {
          capturedUrl = new URL(request.url).pathname;
          return HttpResponse.json({ agent });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok', projectId: 'proj-xyz' });
      await client.agents.create({ name: 'test', projectId: 'proj-xyz' });

      expect(capturedUrl).toBe('/api/v1/projects/proj-xyz/agents');
    });
  });

  describe('get', () => {
    it('fetches a single agent by ID', async () => {
      const agent = makeAgent({ id: 'agent-42', name: 'my-agent' });

      server.use(
        http.get(`${BASE_URL}/api/v1/agents/agent-42`, () => {
          return HttpResponse.json(agent);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.agents.get('agent-42');

      expect(result.id).toBe('agent-42');
      expect(result.name).toBe('my-agent');
    });

    it('throws NotFoundError for 404', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/agents/missing`, () => {
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'agent not found' } },
            { status: 404 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.agents.get('missing')).rejects.toThrow(NotFoundError);
    });

    it('throws AuthenticationError for 401', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/agents/any`, () => {
          return HttpResponse.json(
            { error: { code: 'unauthorized', message: 'unauthorized' } },
            { status: 401 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'bad-tok' });
      await expect(client.agents.get('any')).rejects.toThrow(AuthenticationError);
    });
  });

  describe('list', () => {
    it('fetches agents with default params', async () => {
      const agents = [makeAgent({ id: 'a1' }), makeAgent({ id: 'a2' })];

      server.use(
        http.get(`${BASE_URL}/api/v1/agents`, () => {
          return HttpResponse.json({ agents, totalCount: 2 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const page = await client.agents.list();

      expect(page.data).toHaveLength(2);
      expect(page.totalCount).toBe(2);
      expect(page.hasNext).toBe(false);
    });

    it('passes filter and pagination query params', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/agents`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ agents: [], totalCount: 0 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.list({
        phase: 'running',
        limit: 10,
        cursor: 'abc',
        includeDeleted: true,
        labels: { env: 'prod' },
      });

      expect(capturedUrl).toContain('phase=running');
      expect(capturedUrl).toContain('limit=10');
      expect(capturedUrl).toContain('cursor=abc');
      expect(capturedUrl).toContain('includeDeleted=true');
      expect(capturedUrl).toContain('label=env%3Dprod');
    });

    it('uses project-scoped path', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/projects/:projectId/agents`, ({ request }) => {
          capturedUrl = new URL(request.url).pathname;
          return HttpResponse.json({ agents: [] });
        }),
      );

      const client = new ScionClient({
        hubUrl: BASE_URL,
        token: 'tok',
        projectId: 'proj-1',
      });
      await client.agents.list();

      expect(capturedUrl).toBe('/api/v1/projects/proj-1/agents');
    });

    it('supports async iteration across pages', async () => {
      let callCount = 0;

      server.use(
        http.get(`${BASE_URL}/api/v1/agents`, ({ request }) => {
          callCount++;
          const url = new URL(request.url);
          if (url.searchParams.get('cursor') === 'page2') {
            return HttpResponse.json({
              agents: [makeAgent({ id: 'a3' })],
              totalCount: 3,
            });
          }
          return HttpResponse.json({
            agents: [makeAgent({ id: 'a1' }), makeAgent({ id: 'a2' })],
            nextCursor: 'page2',
            totalCount: 3,
          });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const ids: string[] = [];
      const page = await client.agents.list();

      for await (const agent of page) {
        ids.push(agent.id);
      }

      expect(ids).toEqual(['a1', 'a2', 'a3']);
      expect(callCount).toBe(2);
    });
  });

  describe('lifecycle actions', () => {
    it('start posts to /agents/:id/start', async () => {
      let capturedMethod = '';

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/start`, ({ request }) => {
          capturedMethod = request.method;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.start('agent-1');
      expect(capturedMethod).toBe('POST');
    });

    it('stop posts to /agents/:id/stop', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/stop`, () => {
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.stop('agent-1');
    });

    it('suspend posts to /agents/:id/suspend', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/suspend`, () => {
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.suspend('agent-1');
    });

    it('restart posts to /agents/:id/restart', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/restart`, () => {
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.restart('agent-1');
    });
  });

  describe('delete', () => {
    it('sends DELETE to /agents/:id', async () => {
      let capturedMethod = '';

      server.use(
        http.delete(`${BASE_URL}/api/v1/agents/agent-1`, ({ request }) => {
          capturedMethod = request.method;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.delete('agent-1');
      expect(capturedMethod).toBe('DELETE');
    });
  });

  describe('restore', () => {
    it('posts to /agents/:id/restore and returns the agent', async () => {
      const agent = makeAgent({ id: 'agent-1', phase: 'running' });

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/restore`, () => {
          return HttpResponse.json(agent);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.agents.restore('agent-1');

      expect(result.id).toBe('agent-1');
    });
  });

  describe('sendMessage', () => {
    it('posts a plain text message', async () => {
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/message`, async ({ request }) => {
          capturedBody = await request.json();
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.sendMessage('agent-1', 'hello');

      expect(capturedBody).toEqual({ message: 'hello', interrupt: false });
    });

    it('passes interrupt=true when specified', async () => {
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/message`, async ({ request }) => {
          capturedBody = await request.json();
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.sendMessage('agent-1', 'urgent!', true);

      expect(capturedBody).toEqual({ message: 'urgent!', interrupt: true });
    });
  });

  describe('sendStructuredMessage', () => {
    const structuredMsg: StructuredMessage = {
      version: 1,
      timestamp: '2026-05-12T00:00:00Z',
      sender: 'user:alice',
      recipient: 'agent:code-reviewer',
      msg: 'Please review PR #42',
      type: 'instruction',
    };

    it('posts a structured message', async () => {
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/message`, async ({ request }) => {
          capturedBody = await request.json();
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.sendStructuredMessage('agent-1', structuredMsg);

      expect(capturedBody).toEqual({
        structured_message: structuredMsg,
        interrupt: false,
        notify: false,
      });
    });

    it('passes interrupt and notify options', async () => {
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/agent-1/message`, async ({ request }) => {
          capturedBody = await request.json();
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.sendStructuredMessage('agent-1', structuredMsg, {
        interrupt: true,
        notify: true,
      });

      expect(capturedBody).toEqual({
        structured_message: structuredMsg,
        interrupt: true,
        notify: true,
      });
    });
  });

  describe('broadcastMessage', () => {
    const broadcastMsg: StructuredMessage = {
      version: 1,
      timestamp: '2026-05-12T00:00:00Z',
      sender: 'user:alice',
      recipient: 'all',
      msg: 'Wrap up',
      type: 'instruction',
    };

    it('posts to the project broadcast endpoint', async () => {
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/projects/proj-1/broadcast`, async ({ request }) => {
          capturedBody = await request.json();
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({
        hubUrl: BASE_URL,
        token: 'tok',
        projectId: 'proj-1',
      });
      await client.agents.broadcastMessage(broadcastMsg);

      expect(capturedBody).toEqual({
        structured_message: broadcastMsg,
        interrupt: false,
      });
    });

    it('throws when client is not project-scoped', async () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });

      await expect(client.agents.broadcastMessage(broadcastMsg)).rejects.toThrow(
        'broadcastMessage requires a project-scoped client',
      );
    });
  });

  describe('authentication', () => {
    it('sends bearer token in Authorization header', async () => {
      let capturedAuth: string | null = null;

      server.use(
        http.get(`${BASE_URL}/api/v1/agents/agent-1`, ({ request }) => {
          capturedAuth = request.headers.get('Authorization');
          return HttpResponse.json(makeAgent());
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'my-secret-token' });
      await client.agents.get('agent-1');

      expect(capturedAuth).toBe('Bearer my-secret-token');
    });
  });

  describe('error handling', () => {
    it('throws ValidationError for 400', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/agents`, () => {
          return HttpResponse.json(
            { error: { code: 'validation_error', message: 'invalid name' } },
            { status: 400 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.agents.create({ name: '' })).rejects.toThrow(ValidationError);
    });
  });

  describe('project-scoped paths', () => {
    it('scopes all paths when projectId is set on the client', async () => {
      let capturedPath = '';

      server.use(
        http.post(
          `${BASE_URL}/api/v1/projects/my-project/agents/a1/start`,
          ({ request }) => {
            capturedPath = new URL(request.url).pathname;
            return new HttpResponse(null, { status: 204 });
          },
        ),
      );

      const client = new ScionClient({
        hubUrl: BASE_URL,
        token: 'tok',
        projectId: 'my-project',
      });
      await client.agents.start('a1');

      expect(capturedPath).toContain('/api/v1/projects/my-project/agents/a1/start');
    });

    it('does not scope when projectId is not set', async () => {
      let capturedPath = '';

      server.use(
        http.post(`${BASE_URL}/api/v1/agents/a1/stop`, ({ request }) => {
          capturedPath = new URL(request.url).pathname;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.agents.stop('a1');

      expect(capturedPath).toBe('/api/v1/agents/a1/stop');
      expect(capturedPath).not.toContain('/projects/');
    });
  });

  describe('ScionClient integration', () => {
    it('exposes agents as a property', () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      expect(client.agents).toBeDefined();
      expect(client.agents).toBeInstanceOf(AgentsResource);
      expect(typeof client.agents.list).toBe('function');
      expect(typeof client.agents.get).toBe('function');
      expect(typeof client.agents.create).toBe('function');
      expect(typeof client.agents.delete).toBe('function');
    });
  });
});

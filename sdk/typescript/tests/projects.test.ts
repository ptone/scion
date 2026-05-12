import { describe, it, expect, beforeAll, afterAll, afterEach, vi } from 'vitest';
import { http, HttpResponse } from 'msw';
import { setupServer } from 'msw/node';
import { ScionClient } from '../src/client.js';
import { ProjectsResource } from '../src/resources/projects.js';
import { NotFoundError, ValidationError, ConflictError, ServerError } from '../src/errors.js';
import type { Project, Agent } from '../src/types/index.js';

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

/** Sample project fixture. */
function sampleProject(overrides?: Partial<Project>): Project {
  return {
    id: 'proj-001',
    name: 'test-project',
    slug: 'test-project',
    gitRemote: 'git@github.com:org/repo.git',
    created: '2026-01-01T00:00:00Z',
    updated: '2026-01-02T00:00:00Z',
    visibility: 'private',
    agentCount: 3,
    activeBrokerCount: 1,
    ...overrides,
  };
}

/** Sample agent fixture. */
function sampleAgent(overrides?: Partial<Agent>): Agent {
  return {
    id: 'agent-001',
    slug: 'fix-bug',
    containerId: 'ctr-abc',
    name: 'fix-bug',
    status: 'running',
    phase: 'running',
    projectId: 'proj-001',
    created: '2026-01-01T00:00:00Z',
    updated: '2026-01-02T00:00:00Z',
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('ProjectsResource', () => {
  describe('list', () => {
    it('lists projects with no parameters', async () => {
      const projects = [sampleProject()];

      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, () => {
          return HttpResponse.json({ projects, nextCursor: '', totalCount: 1 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.list();

      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('proj-001');
      expect(result.data[0].name).toBe('test-project');
      expect(result.totalCount).toBe(1);
    });

    it('passes filter parameters as query params', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ projects: [], totalCount: 0 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.projects.list({
        visibility: 'private',
        gitRemote: 'git@github.com:org/repo.git',
        brokerId: 'broker-1',
        name: 'my-project',
        slug: 'my-project',
      });

      expect(capturedUrl).toContain('visibility=private');
      expect(capturedUrl).toContain('brokerId=broker-1');
      expect(capturedUrl).toContain('name=my-project');
      expect(capturedUrl).toContain('slug=my-project');
    });

    it('passes pagination parameters', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({
            projects: [],
            nextCursor: 'cursor-2',
            totalCount: 50,
          });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.list({ limit: 10, cursor: 'cursor-1' });

      expect(capturedUrl).toContain('limit=10');
      expect(capturedUrl).toContain('cursor=cursor-1');
      expect(result.nextCursor).toBe('cursor-2');
      expect(result.totalCount).toBe(50);
    });

    it('passes label filters', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ projects: [], totalCount: 0 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.projects.list({ labels: { team: 'platform' } });

      expect(capturedUrl).toContain('label=team%3Dplatform');
    });

    it('handles empty project list', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, () => {
          return HttpResponse.json({ projects: [], totalCount: 0 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.list();

      expect(result.data).toHaveLength(0);
      expect(result.totalCount).toBe(0);
    });

    it('supports async iteration across pages', async () => {
      let callCount = 0;

      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, ({ request }) => {
          callCount++;
          const url = new URL(request.url);
          if (url.searchParams.get('cursor') === 'page2') {
            return HttpResponse.json({
              projects: [sampleProject({ id: 'proj-3' })],
              totalCount: 3,
            });
          }
          return HttpResponse.json({
            projects: [sampleProject({ id: 'proj-1' }), sampleProject({ id: 'proj-2' })],
            nextCursor: 'page2',
            totalCount: 3,
          });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const ids: string[] = [];
      const page = await client.projects.list();

      for await (const project of page) {
        ids.push(project.id);
      }

      expect(ids).toEqual(['proj-1', 'proj-2', 'proj-3']);
      expect(callCount).toBe(2);
    });
  });

  describe('get', () => {
    it('gets a project by ID', async () => {
      const project = sampleProject();

      server.use(
        http.get(`${BASE_URL}/api/v1/projects/proj-001`, () => {
          return HttpResponse.json(project);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.get('proj-001');

      expect(result.id).toBe('proj-001');
      expect(result.name).toBe('test-project');
      expect(result.gitRemote).toBe('git@github.com:org/repo.git');
    });

    it('throws NotFoundError on 404', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/projects/nonexistent`, () => {
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'Project not found' } },
            { status: 404 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.projects.get('nonexistent')).rejects.toThrow(NotFoundError);
    });
  });

  describe('create', () => {
    it('creates a project with required fields', async () => {
      const created = sampleProject({ id: 'proj-new' });
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/projects`, async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json(created);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.create({ name: 'new-project' });

      expect(capturedBody).toEqual({ name: 'new-project' });
      expect(result.id).toBe('proj-new');
    });

    it('creates a project with all optional fields', async () => {
      const created = sampleProject();
      let capturedBody: unknown;

      server.use(
        http.post(`${BASE_URL}/api/v1/projects`, async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json(created);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.projects.create({
        name: 'full-project',
        id: 'custom-id',
        slug: 'full-proj',
        gitRemote: 'git@github.com:org/repo.git',
        visibility: 'private',
        labels: { env: 'prod' },
      });

      expect(capturedBody).toEqual({
        name: 'full-project',
        id: 'custom-id',
        slug: 'full-proj',
        gitRemote: 'git@github.com:org/repo.git',
        visibility: 'private',
        labels: { env: 'prod' },
      });
    });
  });

  describe('update', () => {
    it('updates a project', async () => {
      const updated = sampleProject({ name: 'updated-name' });
      let capturedBody: unknown;

      server.use(
        http.patch(`${BASE_URL}/api/v1/projects/proj-001`, async ({ request }) => {
          capturedBody = await request.json();
          return HttpResponse.json(updated);
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.update('proj-001', {
        name: 'updated-name',
        labels: { team: 'core' },
      });

      expect(capturedBody).toEqual({ name: 'updated-name', labels: { team: 'core' } });
      expect(result.name).toBe('updated-name');
    });
  });

  describe('delete', () => {
    it('deletes a project', async () => {
      let capturedMethod = '';

      server.use(
        http.delete(`${BASE_URL}/api/v1/projects/proj-001`, ({ request }) => {
          capturedMethod = request.method;
          return new HttpResponse(null, { status: 204 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.projects.delete('proj-001');
      expect(capturedMethod).toBe('DELETE');
    });

    it('throws NotFoundError on 404 delete', async () => {
      server.use(
        http.delete(`${BASE_URL}/api/v1/projects/nonexistent`, () => {
          return HttpResponse.json(
            { error: { code: 'not_found', message: 'Project not found' } },
            { status: 404 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.projects.delete('nonexistent')).rejects.toThrow(NotFoundError);
    });
  });

  describe('listAgents', () => {
    it('lists agents for a project', async () => {
      const agents = [
        sampleAgent(),
        sampleAgent({ id: 'agent-002', name: 'build-feature' }),
      ];

      server.use(
        http.get(`${BASE_URL}/api/v1/projects/proj-001/agents`, () => {
          return HttpResponse.json({ agents, nextCursor: '', totalCount: 2 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      const result = await client.projects.listAgents('proj-001');

      expect(result.data).toHaveLength(2);
      expect(result.data[0].name).toBe('fix-bug');
      expect(result.data[1].name).toBe('build-feature');
      expect(result.totalCount).toBe(2);
    });

    it('passes filter and pagination params', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/projects/proj-001/agents`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ agents: [], totalCount: 0 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.projects.listAgents('proj-001', {
        phase: 'running',
        runtimeBrokerId: 'broker-1',
        limit: 5,
        cursor: 'abc',
      });

      expect(capturedUrl).toContain('phase=running');
      expect(capturedUrl).toContain('runtimeBrokerId=broker-1');
      expect(capturedUrl).toContain('limit=5');
      expect(capturedUrl).toContain('cursor=abc');
    });

    it('passes label filters for agents', async () => {
      let capturedUrl = '';

      server.use(
        http.get(`${BASE_URL}/api/v1/projects/proj-001/agents`, ({ request }) => {
          capturedUrl = request.url;
          return HttpResponse.json({ agents: [], totalCount: 0 });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await client.projects.listAgents('proj-001', {
        labels: { role: 'developer' },
      });

      expect(capturedUrl).toContain('label=role%3Ddeveloper');
    });
  });

  describe('error handling', () => {
    it('throws ValidationError for 400', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/projects`, () => {
          return HttpResponse.json(
            { error: { code: 'validation_error', message: 'Name is required' } },
            { status: 400 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.projects.create({ name: '' })).rejects.toThrow(ValidationError);
    });

    it('throws ConflictError for 409', async () => {
      server.use(
        http.post(`${BASE_URL}/api/v1/projects`, () => {
          return HttpResponse.json(
            { error: { code: 'conflict', message: 'Project already exists' } },
            { status: 409 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.projects.create({ name: 'dup' })).rejects.toThrow(ConflictError);
    });

    it('throws ServerError for 500', async () => {
      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, () => {
          return HttpResponse.json(
            { error: { code: 'internal_error', message: 'Internal server error' } },
            { status: 500 },
          );
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      await expect(client.projects.list()).rejects.toThrow(ServerError);
    });
  });

  describe('authentication', () => {
    it('includes bearer token in all requests', async () => {
      let capturedAuth: string | null = null;

      server.use(
        http.get(`${BASE_URL}/api/v1/projects`, ({ request }) => {
          capturedAuth = request.headers.get('Authorization');
          return HttpResponse.json({ projects: [] });
        }),
      );

      const client = new ScionClient({ hubUrl: BASE_URL, token: 'test-token' });
      await client.projects.list();

      expect(capturedAuth).toBe('Bearer test-token');
    });
  });

  describe('ScionClient integration', () => {
    it('exposes projects as a property', () => {
      const client = new ScionClient({ hubUrl: BASE_URL, token: 'tok' });
      expect(client.projects).toBeDefined();
      expect(client.projects).toBeInstanceOf(ProjectsResource);
      expect(typeof client.projects.list).toBe('function');
      expect(typeof client.projects.get).toBe('function');
      expect(typeof client.projects.create).toBe('function');
      expect(typeof client.projects.update).toBe('function');
      expect(typeof client.projects.delete).toBe('function');
      expect(typeof client.projects.listAgents).toBe('function');
    });
  });
});

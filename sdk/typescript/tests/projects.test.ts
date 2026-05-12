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

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ScionClient } from '../src/client';
import { ScionAPIError } from '../src/errors';
import type { Project } from '../src/types/projects';
import type { Agent } from '../src/types/agents';

/**
 * Create a mock fetch function that returns the given response body.
 * Optionally accepts a status code and validates request properties.
 */
function mockFetch(
  responseBody: unknown,
  status = 200,
  validator?: (url: string, init: RequestInit) => void,
): typeof fetch {
  return vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString();
    if (validator) validator(url, init ?? {});
    return {
      ok: status >= 200 && status < 300,
      status,
      json: async () => responseBody,
      headers: new Headers(),
    } as Response;
  });
}

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
    name: 'fix-bug',
    status: 'running',
    phase: 'running',
    projectId: 'proj-001',
    created: '2026-01-01T00:00:00Z',
    updated: '2026-01-02T00:00:00Z',
    ...overrides,
  };
}

describe('ProjectsResource', () => {
  const BASE_URL = 'https://hub.test.scion.dev';
  const TOKEN = 'test-token';

  function createClient(fetchFn: typeof fetch): ScionClient {
    return new ScionClient({
      baseUrl: BASE_URL,
      token: TOKEN,
      fetch: fetchFn,
    });
  }

  describe('list', () => {
    it('should list projects with no parameters', async () => {
      const projects = [sampleProject()];
      const fetchFn = mockFetch(
        { projects, nextCursor: '', totalCount: 1 },
        200,
        (url, init) => {
          expect(url).toContain('/api/v1/projects');
          expect(init.method).toBe('GET');
          expect(init.headers).toHaveProperty('Authorization', `Bearer ${TOKEN}`);
        },
      );

      const client = createClient(fetchFn);
      const result = await client.projects.list();

      expect(result.data).toHaveLength(1);
      expect(result.data[0].id).toBe('proj-001');
      expect(result.data[0].name).toBe('test-project');
      expect(result.page.totalCount).toBe(1);
    });

    it('should pass filter parameters as query params', async () => {
      const fetchFn = mockFetch(
        { projects: [], totalCount: 0 },
        200,
        (url) => {
          expect(url).toContain('visibility=private');
          expect(url).toContain('gitRemote=git%40github.com');
          expect(url).toContain('brokerId=broker-1');
          expect(url).toContain('name=my-project');
          expect(url).toContain('slug=my-project');
        },
      );

      const client = createClient(fetchFn);
      await client.projects.list({
        visibility: 'private',
        gitRemote: 'git@github.com:org/repo.git',
        brokerId: 'broker-1',
        name: 'my-project',
        slug: 'my-project',
      });

      expect(fetchFn).toHaveBeenCalledTimes(1);
    });

    it('should pass pagination parameters', async () => {
      const fetchFn = mockFetch(
        { projects: [], nextCursor: 'cursor-2', totalCount: 50 },
        200,
        (url) => {
          expect(url).toContain('limit=10');
          expect(url).toContain('cursor=cursor-1');
        },
      );

      const client = createClient(fetchFn);
      const result = await client.projects.list({ limit: 10, cursor: 'cursor-1' });

      expect(result.page.nextCursor).toBe('cursor-2');
      expect(result.page.totalCount).toBe(50);
    });

    it('should pass label filters', async () => {
      const fetchFn = mockFetch(
        { projects: [], totalCount: 0 },
        200,
        (url) => {
          expect(url).toContain('label=');
          expect(url).toContain('team%3Dplatform');
        },
      );

      const client = createClient(fetchFn);
      await client.projects.list({ labels: { team: 'platform' } });

      expect(fetchFn).toHaveBeenCalledTimes(1);
    });

    it('should handle empty project list', async () => {
      const fetchFn = mockFetch({ projects: [], totalCount: 0 });
      const client = createClient(fetchFn);
      const result = await client.projects.list();

      expect(result.data).toHaveLength(0);
      expect(result.page.totalCount).toBe(0);
    });

    it('should handle missing projects field gracefully', async () => {
      const fetchFn = mockFetch({ totalCount: 0 });
      const client = createClient(fetchFn);
      const result = await client.projects.list();

      expect(result.data).toHaveLength(0);
    });
  });

  describe('get', () => {
    it('should get a project by ID', async () => {
      const project = sampleProject();
      const fetchFn = mockFetch(project, 200, (url, init) => {
        expect(url).toContain('/api/v1/projects/proj-001');
        expect(init.method).toBe('GET');
      });

      const client = createClient(fetchFn);
      const result = await client.projects.get('proj-001');

      expect(result.id).toBe('proj-001');
      expect(result.name).toBe('test-project');
      expect(result.gitRemote).toBe('git@github.com:org/repo.git');
    });

    it('should throw ScionAPIError on 404', async () => {
      const fetchFn = mockFetch(
        { code: 'not_found', message: 'Project not found' },
        404,
      );

      const client = createClient(fetchFn);

      await expect(client.projects.get('nonexistent')).rejects.toThrow(ScionAPIError);
      try {
        await client.projects.get('nonexistent');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.isNotFound()).toBe(true);
        expect(apiErr.code).toBe('not_found');
      }
    });
  });

  describe('create', () => {
    it('should create a project with required fields', async () => {
      const created = sampleProject({ id: 'proj-new' });
      const fetchFn = mockFetch(created, 200, (url, init) => {
        expect(url).toContain('/api/v1/projects');
        expect(init.method).toBe('POST');
        const body = JSON.parse(init.body as string);
        expect(body.name).toBe('new-project');
      });

      const client = createClient(fetchFn);
      const result = await client.projects.create({ name: 'new-project' });

      expect(result.id).toBe('proj-new');
    });

    it('should create a project with all optional fields', async () => {
      const created = sampleProject();
      const fetchFn = mockFetch(created, 200, (_url, init) => {
        const body = JSON.parse(init.body as string);
        expect(body.name).toBe('full-project');
        expect(body.id).toBe('custom-id');
        expect(body.slug).toBe('full-proj');
        expect(body.gitRemote).toBe('git@github.com:org/repo.git');
        expect(body.visibility).toBe('private');
        expect(body.labels).toEqual({ env: 'prod' });
      });

      const client = createClient(fetchFn);
      await client.projects.create({
        name: 'full-project',
        id: 'custom-id',
        slug: 'full-proj',
        gitRemote: 'git@github.com:org/repo.git',
        visibility: 'private',
        labels: { env: 'prod' },
      });

      expect(fetchFn).toHaveBeenCalledTimes(1);
    });
  });

  describe('update', () => {
    it('should update a project', async () => {
      const updated = sampleProject({ name: 'updated-name' });
      const fetchFn = mockFetch(updated, 200, (url, init) => {
        expect(url).toContain('/api/v1/projects/proj-001');
        expect(init.method).toBe('PATCH');
        const body = JSON.parse(init.body as string);
        expect(body.name).toBe('updated-name');
        expect(body.labels).toEqual({ team: 'core' });
      });

      const client = createClient(fetchFn);
      const result = await client.projects.update('proj-001', {
        name: 'updated-name',
        labels: { team: 'core' },
      });

      expect(result.name).toBe('updated-name');
    });

    it('should update visibility and defaultRuntimeBrokerId', async () => {
      const updated = sampleProject({
        visibility: 'public',
        defaultRuntimeBrokerId: 'broker-1',
      });
      const fetchFn = mockFetch(updated, 200, (_url, init) => {
        const body = JSON.parse(init.body as string);
        expect(body.visibility).toBe('public');
        expect(body.defaultRuntimeBrokerId).toBe('broker-1');
      });

      const client = createClient(fetchFn);
      const result = await client.projects.update('proj-001', {
        visibility: 'public',
        defaultRuntimeBrokerId: 'broker-1',
      });

      expect(result.visibility).toBe('public');
    });
  });

  describe('delete', () => {
    it('should delete a project', async () => {
      const fetchFn = mockFetch(undefined, 204, (url, init) => {
        expect(url).toContain('/api/v1/projects/proj-001');
        expect(init.method).toBe('DELETE');
      });

      const client = createClient(fetchFn);
      await expect(client.projects.delete('proj-001')).resolves.toBeUndefined();
    });

    it('should throw ScionAPIError on 404 delete', async () => {
      const fetchFn = mockFetch(
        { code: 'not_found', message: 'Project not found' },
        404,
      );

      const client = createClient(fetchFn);
      await expect(client.projects.delete('nonexistent')).rejects.toThrow(ScionAPIError);
    });
  });

  describe('listAgents', () => {
    it('should list agents for a project', async () => {
      const agents = [sampleAgent(), sampleAgent({ id: 'agent-002', name: 'build-feature' })];
      const fetchFn = mockFetch(
        { agents, nextCursor: '', totalCount: 2 },
        200,
        (url, init) => {
          expect(url).toContain('/api/v1/projects/proj-001/agents');
          expect(init.method).toBe('GET');
        },
      );

      const client = createClient(fetchFn);
      const result = await client.projects.listAgents('proj-001');

      expect(result.data).toHaveLength(2);
      expect(result.data[0].name).toBe('fix-bug');
      expect(result.data[1].name).toBe('build-feature');
      expect(result.page.totalCount).toBe(2);
    });

    it('should pass filter and pagination params', async () => {
      const fetchFn = mockFetch(
        { agents: [], totalCount: 0 },
        200,
        (url) => {
          expect(url).toContain('phase=running');
          expect(url).toContain('runtimeBrokerId=broker-1');
          expect(url).toContain('limit=5');
          expect(url).toContain('cursor=abc');
        },
      );

      const client = createClient(fetchFn);
      await client.projects.listAgents('proj-001', {
        phase: 'running',
        runtimeBrokerId: 'broker-1',
        limit: 5,
        cursor: 'abc',
      });

      expect(fetchFn).toHaveBeenCalledTimes(1);
    });

    it('should handle empty agent list', async () => {
      const fetchFn = mockFetch({ agents: [], totalCount: 0 });
      const client = createClient(fetchFn);
      const result = await client.projects.listAgents('proj-001');

      expect(result.data).toHaveLength(0);
    });

    it('should pass label filters for agents', async () => {
      const fetchFn = mockFetch(
        { agents: [], totalCount: 0 },
        200,
        (url) => {
          expect(url).toContain('label=');
          expect(url).toContain('role%3Ddeveloper');
        },
      );

      const client = createClient(fetchFn);
      await client.projects.listAgents('proj-001', {
        labels: { role: 'developer' },
      });

      expect(fetchFn).toHaveBeenCalledTimes(1);
    });
  });

  describe('authentication', () => {
    it('should include bearer token in all requests', async () => {
      const fetchFn = mockFetch({ projects: [] }, 200, (_url, init) => {
        const headers = init.headers as Record<string, string>;
        expect(headers['Authorization']).toBe('Bearer test-token');
      });

      const client = createClient(fetchFn);
      await client.projects.list();
    });

    it('should work without a token', async () => {
      const fetchFn = mockFetch({ projects: [] }, 200, (_url, init) => {
        const headers = init.headers as Record<string, string>;
        expect(headers['Authorization']).toBeUndefined();
      });

      const client = new ScionClient({
        baseUrl: BASE_URL,
        fetch: fetchFn,
      });
      await client.projects.list();
    });
  });

  describe('error handling', () => {
    it('should throw ScionAPIError with parsed error body', async () => {
      const fetchFn = mockFetch(
        {
          code: 'validation_error',
          message: 'Name is required',
          details: { field: 'name' },
          requestId: 'req-123',
        },
        400,
      );

      const client = createClient(fetchFn);

      try {
        await client.projects.create({ name: '' });
        expect.fail('Should have thrown');
      } catch (err) {
        expect(err).toBeInstanceOf(ScionAPIError);
        const apiErr = err as ScionAPIError;
        expect(apiErr.statusCode).toBe(400);
        expect(apiErr.code).toBe('validation_error');
        expect(apiErr.message).toBe('Name is required');
        expect(apiErr.details).toEqual({ field: 'name' });
        expect(apiErr.requestId).toBe('req-123');
      }
    });

    it('should handle 401 unauthorized', async () => {
      const fetchFn = mockFetch(
        { code: 'unauthorized', message: 'Invalid token' },
        401,
      );

      const client = createClient(fetchFn);

      try {
        await client.projects.list();
        expect.fail('Should have thrown');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.isUnauthorized()).toBe(true);
      }
    });

    it('should handle 403 forbidden', async () => {
      const fetchFn = mockFetch(
        { code: 'forbidden', message: 'Access denied' },
        403,
      );

      const client = createClient(fetchFn);

      try {
        await client.projects.get('proj-001');
        expect.fail('Should have thrown');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.isForbidden()).toBe(true);
      }
    });

    it('should handle 409 conflict', async () => {
      const fetchFn = mockFetch(
        { code: 'conflict', message: 'Project already exists' },
        409,
      );

      const client = createClient(fetchFn);

      try {
        await client.projects.create({ name: 'dup' });
        expect.fail('Should have thrown');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.isConflict()).toBe(true);
      }
    });

    it('should handle 429 rate limited', async () => {
      const fetchFn = mockFetch(
        { code: 'rate_limited', message: 'Too many requests' },
        429,
      );

      const client = createClient(fetchFn);

      try {
        await client.projects.list();
        expect.fail('Should have thrown');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.isRateLimited()).toBe(true);
      }
    });

    it('should handle 500 server error', async () => {
      const fetchFn = mockFetch(
        { code: 'internal_error', message: 'Internal server error' },
        500,
      );

      const client = createClient(fetchFn);

      try {
        await client.projects.list();
        expect.fail('Should have thrown');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.isServerError()).toBe(true);
      }
    });

    it('should handle non-JSON error responses', async () => {
      const fetchFn = vi.fn(async () => ({
        ok: false,
        status: 502,
        json: async () => {
          throw new Error('not json');
        },
        headers: new Headers(),
      })) as unknown as typeof fetch;

      const client = createClient(fetchFn);

      try {
        await client.projects.list();
        expect.fail('Should have thrown');
      } catch (err) {
        const apiErr = err as ScionAPIError;
        expect(apiErr.statusCode).toBe(502);
        expect(apiErr.code).toBe('unknown_error');
      }
    });
  });
});

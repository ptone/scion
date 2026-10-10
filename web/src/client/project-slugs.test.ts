/**
 * Copyright 2026 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';

vi.mock('./api.js', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./api.js')>();
  return { ...actual, apiFetch: vi.fn() };
});

import { apiFetch } from './api.js';
import { ProjectSlugIndex, fetchAllProjectSlugs, type ProjectSlugSource } from './project-slugs.js';

const apiFetchMock = vi.mocked(apiFetch);

afterEach(() => {
  apiFetchMock.mockReset();
  vi.restoreAllMocks();
});

function deferred<T>(): {
  promise: Promise<T>;
  resolve: (v: T) => void;
  reject: (e: unknown) => void;
} {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('ProjectSlugIndex', () => {
  it('looks up seeded slugs and skips projects without one', () => {
    const index = new ProjectSlugIndex(vi.fn());
    index.seed([{ id: 'p1', slug: 'alpha' }, { id: 'p2' }]);

    expect(index.lookup('p1')).toBe('alpha');
    expect(index.lookup('p2')).toBeUndefined();
  });

  it('notifies subscribers and bumps the version only when a slug changes', () => {
    const index = new ProjectSlugIndex(vi.fn());
    const listener = vi.fn();
    const unsubscribe = index.subscribe(listener);

    index.seed([{ id: 'p1', slug: 'alpha' }]);
    const version = index.version;
    index.seed([{ id: 'p1', slug: 'alpha' }]);

    expect(listener).toHaveBeenCalledTimes(1);
    expect(index.version).toBe(version);

    unsubscribe();
    index.seed([{ id: 'p1', slug: 'renamed' }]);
    expect(listener).toHaveBeenCalledTimes(1);
    expect(index.lookup('p1')).toBe('renamed');
  });

  it('does not list projects when every slug is known', async () => {
    const fetchProjects = vi.fn<() => Promise<ProjectSlugSource[]>>();
    const index = new ProjectSlugIndex(fetchProjects);
    index.seed([{ id: 'p1', slug: 'alpha' }]);

    await index.ensure(['p1', '']);

    expect(fetchProjects).not.toHaveBeenCalled();
  });

  it('lists projects once for unknown slugs, sharing a walk in flight', async () => {
    const walk = deferred<ProjectSlugSource[]>();
    const fetchProjects = vi.fn(() => walk.promise);
    const index = new ProjectSlugIndex(fetchProjects);
    const listener = vi.fn();
    index.subscribe(listener);

    const first = index.ensure(['p1']);
    const second = index.ensure(['p2']);
    walk.resolve([
      { id: 'p1', slug: 'alpha' },
      { id: 'p2', slug: 'beta' },
    ]);
    await Promise.all([first, second]);

    expect(fetchProjects).toHaveBeenCalledTimes(1);
    expect(index.lookup('p1')).toBe('alpha');
    expect(index.lookup('p2')).toBe('beta');
    expect(listener).toHaveBeenCalledTimes(1);

    // A project the viewer cannot list stays unknown without another walk.
    await index.ensure(['p3']);
    expect(fetchProjects).toHaveBeenCalledTimes(1);
  });

  it('resolves on a failed walk and walks again on the next ensure', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const fetchProjects = vi
      .fn<() => Promise<ProjectSlugSource[]>>()
      .mockRejectedValueOnce(new Error('unavailable'))
      .mockResolvedValueOnce([{ id: 'p1', slug: 'alpha' }]);
    const index = new ProjectSlugIndex(fetchProjects);

    await expect(index.ensure(['p1'])).resolves.toBeUndefined();
    expect(index.lookup('p1')).toBeUndefined();

    await index.ensure(['p1']);
    expect(fetchProjects).toHaveBeenCalledTimes(2);
    expect(index.lookup('p1')).toBe('alpha');
  });
});

describe('fetchAllProjectSlugs', () => {
  it('walks every page of the project list', async () => {
    apiFetchMock
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ projects: [{ id: 'p1', slug: 'alpha' }], nextCursor: 'c2' }))
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ projects: [{ id: 'p2', slug: 'beta' }] }))
      );

    const projects = await fetchAllProjectSlugs();

    expect(projects.map((p) => p.slug)).toEqual(['alpha', 'beta']);
    const urls = apiFetchMock.mock.calls.map((call) => String(call[0]));
    expect(urls[0]).toMatch(/^\/api\/v1\/projects\?limit=\d+$/);
    expect(urls[1]).toContain('cursor=c2');
  });
});

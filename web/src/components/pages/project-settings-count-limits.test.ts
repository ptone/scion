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

/**
 * Project default count limits (ptone/scion#3925): each is Inherit (null),
 * Unlimited (0) or a limit. The page loads a stored 0 as Unlimited, sends 0
 * for Unlimited and null to clear, and leaves an untouched field out of the
 * save, so a save from this page never changes a limit it did not edit.
 */

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { requestUrl } from '../../client/__fixtures__/request-url.js';
import { countLimitOrNull, parseCountLimitInput } from './project-settings.js';

interface Internals {
  projectId: string;
  updateComplete: Promise<unknown>;
  handleSaveConfig: () => Promise<void>;
  configDefaultMaxTurns: number | null;
  configDefaultMaxModelCalls: number | null;
}

let stored: Record<string, unknown> = {};
let puts: Array<Record<string, unknown>> = [];

function stubFetch(): void {
  const json = (body: unknown, status = 200): Promise<Response> =>
    Promise.resolve(
      new Response(JSON.stringify(body), {
        status,
        headers: { 'Content-Type': 'application/json' },
      })
    );
  vi.stubGlobal(
    'fetch',
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = new URL(requestUrl(input), 'http://hub.example.com').pathname;
      if (init?.method === 'PUT' && path.endsWith('/settings') && typeof init.body === 'string') {
        const body = JSON.parse(init.body) as Record<string, unknown>;
        puts.push(body);
        return json(body);
      }
      if (path.endsWith('/settings/resolved')) {
        return json({ projectId: 'proj-1', project: stored, settings: {} });
      }
      if (path.endsWith('/settings')) return json(stored);
      if (path.includes('/templates')) {
        return json({ templates: [], page: 1, pageSize: 100, total: 0 });
      }
      if (/\/api\/v1\/projects\/[^/]+$/.test(path)) {
        return json({
          id: 'proj-1',
          name: 'Test Project',
          slug: 'test-project',
          _capabilities: { actions: ['update', 'manage'] },
        });
      }
      return json({});
    })
  );
}

beforeAll(async () => {
  stubFetch();
  await import('./project-settings.js');
}, 30_000);

afterEach(() => {
  document.body.innerHTML = '';
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mount(settings: Record<string, unknown>): Promise<HTMLElement & Internals> {
  stored = settings;
  puts = [];
  stubFetch();
  vi.spyOn(console, 'error').mockImplementation(() => {});
  const el = document.createElement('scion-page-project-settings') as HTMLElement & Internals;
  el.projectId = 'proj-1';
  document.body.appendChild(el);
  await el.updateComplete;
  await new Promise((resolve) => setTimeout(resolve, 400));
  await el.updateComplete;
  return el;
}

describe('count-limit helpers', () => {
  it('maps stored values to page state', () => {
    expect(countLimitOrNull(undefined)).toBeNull();
    expect(countLimitOrNull(null)).toBeNull();
    expect(countLimitOrNull(0)).toBe(0);
    expect(countLimitOrNull(12)).toBe(12);
    expect(countLimitOrNull(-1)).toBeNull();
  });

  it('parses only positive integers from the input; anything else is Inherit', () => {
    expect(parseCountLimitInput('7')).toBe(7);
    expect(parseCountLimitInput(' 40 ')).toBe(40);
    expect(parseCountLimitInput('')).toBeNull();
    expect(parseCountLimitInput('0')).toBeNull();
    expect(parseCountLimitInput('-3')).toBeNull();
    expect(parseCountLimitInput('1.5')).toBeNull();
  });
});

describe('project settings count limits', () => {
  it('loads a stored 0 as Unlimited and an absent value as Inherit', async () => {
    const el = await mount({ defaultMaxTurns: 0 });
    expect(el.configDefaultMaxTurns).toBe(0);
    expect(el.configDefaultMaxModelCalls).toBeNull();
    const boxes = Array.from(el.shadowRoot!.querySelectorAll('sl-checkbox.unlimited'));
    expect(boxes).toHaveLength(2);
    expect(boxes[0]!.hasAttribute('checked')).toBe(true);
    expect(boxes[1]!.hasAttribute('checked')).toBe(false);
  });

  it('leaves untouched count limits out of a save', async () => {
    const el = await mount({ defaultMaxTurns: 0, defaultMaxModelCalls: 20 });
    await el.handleSaveConfig();
    expect(puts).toHaveLength(1);
    expect('defaultMaxTurns' in puts[0]!).toBe(false);
    expect('defaultMaxModelCalls' in puts[0]!).toBe(false);
  });

  it('never sends 0 for an empty, untouched field (the pre-upgrade stale-tab case)', async () => {
    const el = await mount({});
    await el.handleSaveConfig();
    expect(puts).toHaveLength(1);
    expect('defaultMaxTurns' in puts[0]!).toBe(false);
    expect('defaultMaxModelCalls' in puts[0]!).toBe(false);
  });

  it('sends 0 for Unlimited, null to clear and n for a limit', async () => {
    const el = await mount({ defaultMaxModelCalls: 20 });
    el.configDefaultMaxTurns = 0;
    el.configDefaultMaxModelCalls = null;
    await el.handleSaveConfig();
    expect(puts[0]!.defaultMaxTurns).toBe(0);
    expect(puts[0]!.defaultMaxModelCalls).toBeNull();

    const el2 = await mount({ defaultMaxTurns: 0 });
    el2.configDefaultMaxTurns = 15;
    await el2.handleSaveConfig();
    expect(puts[0]!.defaultMaxTurns).toBe(15);
    expect('defaultMaxModelCalls' in puts[0]!).toBe(false);
  });
});

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

/**
 * Covers the small and paged window states: off-page upsert,
 * reconnect chip with no request, label typing/commit, a label 400 keeping
 * previous data, lifecycle refresh issuing exactly one request, and the 422
 * fallback with its per-label memory. Small-state display being identical
 * to the pre-change `displayAgents` is covered via `agent-sort.test.ts`'s
 * parity checks and the list/grid-identity assertions in the test files.
 *
 * Shared helpers and hooks for the project-detail-agent-window.*.test.ts files.
 */

import { expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import type { Agent, Capabilities, PageData } from '../../../shared/types.js';
import { resetHubProjectCapabilitiesCache } from '../../../client/hub-capabilities.js';
import { stateManager } from '../../../client/state.js';
import type { AgentListWindow } from '../../../client/agent-list-window.js';
import { AgentDrainRunner } from '../../../client/agent-drain.js';

/**
 * happy-dom has no EventSource; setScope opens one. It opens on the next
 * tick, so a drain's wait for the live connection resolves at once.
 */
export class FakeEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = FakeEventSource.CONNECTING;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  constructor(readonly url: string) {
    super();
    setTimeout(() => {
      if (this.readyState === FakeEventSource.CLOSED) return;
      this.readyState = FakeEventSource.OPEN;
      this.onopen?.(new Event('open'));
    }, 0);
  }
  close(): void {
    this.readyState = FakeEventSource.CLOSED;
  }
}

export function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

/**
 * Re-stubs fetch so a sorted agents GET answers with a body that has no
 * `agents` field; every other request still reaches `inner`.
 */
export function stubSortedAgentsWithoutList(projectId: string, inner: typeof fetch): void {
  stubFetch((input: string | URL | Request, init?: RequestInit) => {
    const rawUrl =
      typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const u = new URL(rawUrl, 'http://localhost');
    if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
      return Promise.resolve(jsonResponse({ totalCount: 0, complete: true }));
    }
    return inner(input, init);
  });
}

export function makeAgent(i: number, overrides: Partial<Agent> = {}): Agent {
  return {
    id: `a-${i}`,
    name: `agent-${i}`,
    projectId: 'p-win',
    template: 't',
    phase: 'running',
    created: `2026-01-01T00:00:${String(i % 60).padStart(2, '0')}Z`,
    updated: `2026-01-02T00:00:${String(i % 60).padStart(2, '0')}Z`,
    messageMode: 'project',
    _capabilities: { actions: ['read', 'update', 'delete', 'stop_all'] },
    ...overrides,
  } as Agent;
}

export interface AgentsRequest {
  url: string;
}

/** A minimal fake project-agents endpoint implementing enough of the project agents endpoint contract for these tests. */
export function createFetchHandler(opts: {
  projectId: string;
  projectCaps: Capabilities;
  agents: Agent[];
  requests: AgentsRequest[];
  refuseSortedForLabel?: string;
}) {
  return (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const rawUrl =
      typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const u = new URL(rawUrl, 'http://localhost');
    const path = u.pathname;
    const method = (
      init?.method ?? (input instanceof Request ? input.method : 'GET')
    ).toUpperCase();

    if (path.includes('/api/v1/projects?limit=1')) {
      return Promise.resolve(jsonResponse({ projects: [] }));
    }

    if (path === `/api/v1/projects/${opts.projectId}/agents` && method === 'GET') {
      opts.requests.push({ url: rawUrl });
      const sort = u.searchParams.get('sort');
      const label = u.searchParams.get('label') ?? '';

      if (sort) {
        if (opts.refuseSortedForLabel !== undefined && label === opts.refuseSortedForLabel) {
          return Promise.resolve(
            jsonResponse(
              {
                error: {
                  code: 'sorted_view_unavailable',
                  message: 'too many agents for a sorted view',
                  details: { reason: 'too_many_candidates' },
                },
              },
              422
            )
          );
        }
        let list = opts.agents;
        if (label.includes('=')) {
          const [k, v] = label.split('=');
          list = list.filter((a) => a.labels?.[k] === v);
        }
        const dir = u.searchParams.get('dir') === 'asc' ? 1 : -1;
        const sorted = [...list].sort(
          (a, b) => dir * (a.updated ?? '').localeCompare(b.updated ?? '')
        );
        return Promise.resolve(
          jsonResponse({
            agents: sorted,
            totalCount: sorted.length,
            complete: true,
            stats: {
              total: sorted.length,
              running: sorted.filter((a) => a.phase === 'running').length,
              agents: sorted.map((a) => [a.id, a.phase]),
            },
            _capabilities: opts.projectCaps,
          })
        );
      }

      // Legacy mode.
      let list = opts.agents;
      if (label.includes('=')) {
        const [k, v] = label.split('=');
        list = list.filter((a) => a.labels?.[k] === v);
      }
      return Promise.resolve(jsonResponse({ agents: list, _capabilities: opts.projectCaps }));
    }

    if (path === `/api/v1/projects/${opts.projectId}` && method === 'GET') {
      return Promise.resolve(
        jsonResponse({
          id: opts.projectId,
          name: 'Window Project',
          slug: 'window-project',
          _capabilities: opts.projectCaps,
        })
      );
    }

    if (/^\/api\/v1\/agents\/[^/]+\/(start|stop|suspend)$/.test(path) && method === 'POST') {
      return Promise.resolve(jsonResponse({}));
    }

    if (path === `/api/v1/projects/${opts.projectId}/agents/stop-all` && method === 'POST') {
      return Promise.resolve(jsonResponse({ stopped: 0, failed: 0 }));
    }

    return Promise.resolve(jsonResponse({}, 404));
  };
}

export type TestEl = HTMLElement & {
  updateComplete: Promise<boolean>;
  pageData: PageData | null;
  projectId: string;
};

/** The URL a fetch call asked for. */
export function requestUrl(input: string | URL | Request): string {
  return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
}

/**
 * URLs of the requests sent through stubFetch in the current test, kept as
 * plain strings and cleared after each test.
 */
export const sentUrls: string[] = [];

/**
 * Stubs fetch with a plain function, not vi.fn(): Vitest keeps every
 * vi.fn() and its recorded calls until the file ends, and a recorded
 * request's abort signal reaches the page that sent it, so each test's
 * page and agents would stay on the heap. Only the URL is recorded.
 */
export function stubFetch(
  handler: (input: string | URL | Request, init?: RequestInit) => Promise<Response>
): void {
  vi.stubGlobal('fetch', (input: string | URL | Request, init?: RequestInit) => {
    sentUrls.push(requestUrl(input));
    return handler(input, init);
  });
}

/** Whether the stubbed fetch has been asked for this project's agents list. */
export function agentsListRequested(projectId: string): boolean {
  return sentUrls.some(
    (raw) => new URL(raw, 'http://localhost').pathname === `/api/v1/projects/${projectId}/agents`
  );
}

/**
 * Mounts the page and waits until its first agents request was sent and,
 * unless the test holds that response open (`holdsFirstLoad`), until the
 * page is idle.
 */
export async function createComponent(
  projectId: string,
  opts: { holdsFirstLoad?: boolean } = {}
): Promise<TestEl> {
  const el = document.createElement('scion-page-project-detail') as TestEl;
  el.projectId = projectId;
  // Drain retries without a delay.
  (el as unknown as { drainRunner: AgentDrainRunner }).drainRunner = new AgentDrainRunner({
    retryDelayMs: 0,
  });
  el.pageData = {
    path: `/projects/${projectId}`,
    title: 'Project',
    user: { id: 'u', email: 'u@example.com', name: 'U', role: 'member' },
  };
  document.body.appendChild(el);
  await el.updateComplete;
  await vi.waitFor(() => expect(agentsListRequested(projectId)).toBe(true));
  if (!opts.holdsFirstLoad) await settle(el);
  await el.updateComplete;
  return el;
}

export function labelInput(el: TestEl): (HTMLElement & { value: string }) | null {
  const inputs = Array.from(el.shadowRoot?.querySelectorAll('sl-input') ?? []);
  return (inputs.find((i) => i.getAttribute('placeholder') === 'Filter by label (key=value)') ??
    null) as (HTMLElement & { value: string }) | null;
}

export function viewToggle(el: TestEl): HTMLElement | null {
  return el.shadowRoot?.querySelector('scion-view-toggle') ?? null;
}

export interface Internals {
  toggleSort(field: string): void;
  setPhaseFilter(phase: string): void;
  handleAgentAction(id: string, action: string): Promise<void>;
  backgroundRefresh(trigger: string): void;
  handleStopAll(): Promise<void>;
  onPagerSizeChange(size: number): void;
  committedLabel: string;
  pagerPageSize: number;
  agents: Agent[];
  agentWindow: {
    next(): Promise<void>;
    prev(): Promise<void>;
    refresh(): Promise<void>;
    state: 'small' | 'paged' | 'held' | 'capped';
    items: Agent[];
    display: Agent[];
    pageIndex: number;
    hasNext: boolean;
    hasPrev: boolean;
    memberIndex: AgentListWindow['memberIndex'];
    planRequest: AgentListWindow['planRequest'];
    updatesAvailable: boolean;
    error: string | null;
    loading: boolean;
    stale: boolean;
  };
  agentStats: { total: number; running: number };
  agentsLoading: boolean;
}

export function internals(el: TestEl): Internals {
  return el as unknown as Internals;
}

export function pager(el: TestEl): (HTMLElement & { pageSize: number }) | null {
  return el.shadowRoot?.querySelector('scion-agent-pager') as
    | (HTMLElement & { pageSize: number })
    | null;
}

/** A deferred promise, for controlling fetch resolution order explicitly. */
/** Runs state.ts's pending coalesced flush now, then lets the page re-render. */
export async function flushLive(el: TestEl): Promise<void> {
  (stateManager as unknown as { flush(): void }).flush();
  await Promise.resolve();
  await el.updateComplete;
}

/**
 * Waits until no agents load is in flight (page-level or the window's own
 * page fetch), then runs the pending live flush and lets the page render.
 */
export async function settle(el: TestEl): Promise<void> {
  await vi.waitFor(
    () => {
      expect(internals(el).agentsLoading).toBe(false);
      expect(internals(el).agentWindow.loading).toBe(false);
    },
    { timeout: 10_000 }
  );
  await flushLive(el);
}

export function deferred<T>(): { promise: Promise<T>; resolve: (v: T) => void } {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

/**
 * A more realistic sorted-mode handler than `createFetchHandler`'s:
 * `complete` is decided by `agents.length` vs
 * `fit`, cursor pages are real continuations of the same sorted list, and
 * `legacyTruncated` simulates a >500-candidate legacy response without
 * needing a 501-agent fixture.
 */
export function createRealisticFetchHandler(opts: {
  projectId: string;
  projectCaps: Capabilities;
  agents: Agent[];
  requests: AgentsRequest[];
  legacyTruncated?: boolean;
  /** Sorted requests over more candidates than this answer 422 (the server's 2,000 candidate ceiling). */
  refuseSortedAbove?: number;
  /** Legacy pages return only the agents passing this (the server's per-item read filter). */
  readable?: (a: Agent) => boolean;
  /** A legacy request with this cursor answers 500. */
  failLegacyCursor?: string;
}) {
  return (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const rawUrl =
      typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const u = new URL(rawUrl, 'http://localhost');
    const path = u.pathname;
    const method = (
      init?.method ?? (input instanceof Request ? input.method : 'GET')
    ).toUpperCase();

    if (path.includes('/api/v1/projects?limit=1')) {
      return Promise.resolve(jsonResponse({ projects: [] }));
    }

    if (path === `/api/v1/projects/${opts.projectId}` && method === 'GET') {
      return Promise.resolve(
        jsonResponse({
          id: opts.projectId,
          name: 'Window Project',
          slug: 'window-project',
          _capabilities: opts.projectCaps,
        })
      );
    }

    if (/^\/api\/v1\/agents\/[^/]+\/(start|stop|suspend)$/.test(path) && method === 'POST') {
      return Promise.resolve(jsonResponse({}));
    }
    if (path === `/api/v1/projects/${opts.projectId}/agents/stop-all` && method === 'POST') {
      return Promise.resolve(jsonResponse({ stopped: 0, failed: 0 }));
    }

    if (path === `/api/v1/projects/${opts.projectId}/agents` && method === 'GET') {
      opts.requests.push({ url: rawUrl });
      const sort = u.searchParams.get('sort');
      const label = u.searchParams.get('label') ?? '';
      const phase = u.searchParams.get('phase') ?? '';
      let list = opts.agents;
      if (label.includes('=')) {
        const eq = label.indexOf('=');
        const k = label.slice(0, eq);
        const v = label.slice(eq + 1);
        list = list.filter((a) => a.labels?.[k] === v);
      }

      if (sort) {
        if (opts.refuseSortedAbove !== undefined && list.length > opts.refuseSortedAbove) {
          return Promise.resolve(
            jsonResponse(
              {
                error: {
                  code: 'sorted_view_unavailable',
                  message: 'too many agents for a sorted view',
                  details: { reason: 'too_many_candidates' },
                },
              },
              422
            )
          );
        }
        const dir = u.searchParams.get('dir') === 'asc' ? 1 : -1;
        const sorted = [...list].sort(
          (a, b) => dir * (a.updated ?? '').localeCompare(b.updated ?? '')
        );
        const cursor = u.searchParams.get('cursor');
        const limit = Number(u.searchParams.get('limit') ?? '25');
        const statsOf = (set: Agent[]) => ({
          total: set.length,
          running: set.filter((a) => a.phase === 'running').length,
          agents: set.map((a) => [a.id, a.phase]) as Array<[string, string]>,
        });

        // Paged (incomplete) responses are phase-filtered server-side; a
        // complete response is always the whole unphased set.
        const phased = phase ? sorted.filter((a) => a.phase === phase) : sorted;

        // ids= (a page of the window's frozen walk order): exactly the named
        // agents that still match, with the other filters applied.
        const idsParam = u.searchParams.get('ids');
        if (idsParam !== null) {
          const wanted = new Set(idsParam.split(','));
          const page = phased.filter((a) => wanted.has(a.id));
          return Promise.resolve(
            jsonResponse({
              agents: page,
              totalCount: page.length,
              complete: false,
              stats: u.searchParams.get('stats') ? statsOf(page) : undefined,
            })
          );
        }

        if (cursor !== null) {
          const startIdx = Number(cursor);
          const page = phased.slice(startIdx, startIdx + limit);
          const nextIdx = startIdx + limit;
          return Promise.resolve(
            jsonResponse({
              agents: page,
              totalCount: phased.length,
              complete: false,
              nextCursor: nextIdx < phased.length ? String(nextIdx) : undefined,
              stats: u.searchParams.get('stats') ? statsOf(sorted) : undefined,
            })
          );
        }

        // The server only reports a complete set when fit was sent; a sorted
        // request without fit (the window's own page fetches) is always a page.
        const fitParam = u.searchParams.get('fit');
        if (fitParam !== null && sorted.length <= Number(fitParam)) {
          return Promise.resolve(
            jsonResponse({
              agents: sorted,
              totalCount: sorted.length,
              complete: true,
              stats: statsOf(sorted),
              _capabilities: opts.projectCaps,
            })
          );
        }
        const page = phased.slice(0, limit);
        return Promise.resolve(
          jsonResponse({
            agents: page,
            totalCount: phased.length,
            complete: false,
            nextCursor: limit < phased.length ? String(limit) : undefined,
            stats: statsOf(sorted),
          })
        );
      }

      // Legacy mode: cursor pages of the unsorted list. `legacyTruncated`
      // shrinks a legacy page to 20 items, so a 100-agent drain needs five
      // pages and is capped at four.
      const legacyLimit = opts.legacyTruncated ? 20 : Number(u.searchParams.get('limit') ?? '500');
      if (
        opts.failLegacyCursor !== undefined &&
        u.searchParams.get('cursor') === opts.failLegacyCursor
      ) {
        return Promise.resolve(jsonResponse({ error: { message: 'boom' } }, 500));
      }
      const start = Number(u.searchParams.get('cursor') ?? '0');
      const end = start + legacyLimit;
      const readable = opts.readable;
      const pageRows = list.slice(start, end);
      return Promise.resolve(
        jsonResponse({
          agents: readable ? pageRows.filter(readable) : pageRows,
          nextCursor: end < list.length ? String(end) : undefined,
          _capabilities: opts.projectCaps,
        })
      );
    }

    return Promise.resolve(jsonResponse({}, 404));
  };
}

/**
 * Stops the state store from scheduling its coalesced flush on its own, so
 * a live update stays unflushed (tombstoned, but with no agents-changed)
 * until the test flushes it. Returns a restore function.
 */
export function deferLiveFlush(): () => void {
  const sm = stateManager as unknown as { scheduleFlush(): void; flushScheduled: boolean };
  const spy = vi.spyOn(sm, 'scheduleFlush').mockImplementation(function (this: {
    flushScheduled: boolean;
  }) {
    this.flushScheduled = true;
  });
  return () => spy.mockRestore();
}

/**
 * Records, for the first agents-changed that carries `id` as deleted,
 * whether the page's agent window was still loading at that moment.
 */
export function watchDeleteFlush(
  id: string,
  loading: () => boolean
): { loadingAtFlush?: boolean; stop(): void } {
  const out: { loadingAtFlush?: boolean; stop(): void } = { stop: () => {} };
  const onChanged = (e: Event): void => {
    const deleted = (e as CustomEvent<{ data?: { deleted?: string[] } }>).detail?.data?.deleted;
    if (out.loadingAtFlush === undefined && deleted?.includes(id)) out.loadingAtFlush = loading();
  };
  stateManager.addEventListener('agents-changed', onChanged);
  out.stop = () => stateManager.removeEventListener('agents-changed', onChanged);
  return out;
}

/** Registers the suite's hooks; call it first inside the outer describe. */
export function useProjectDetailWindowHooks(): void {
  beforeAll(async () => {
    await import('../project-detail.js');
  }, 60_000);

  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeEventSource);
    resetHubProjectCapabilitiesCache();
  });

  afterEach(() => {
    document.body.querySelectorAll('scion-page-project-detail').forEach((n) => n.remove());
    sentUrls.length = 0;
    // Vitest keeps every vi.fn() and spy, with its recorded calls, until
    // the file ends. Clear the calls so they do not keep removed pages and
    // their agents reachable.
    vi.clearAllMocks();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    localStorage.clear();
  });
}

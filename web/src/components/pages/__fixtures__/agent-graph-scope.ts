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
 * The standalone agent graph's load path, scope and live membership:
 * - a held complete set renders with no request, for any project filter;
 * - an unscoped graph drains every agent in the compact view;
 * - a scoped graph sends one fit probe, then drains only its project when
 *   the probe is not complete;
 * - picker changes abort the in-flight load, never repeat the probe, and
 *   reuse a capped unscoped set for "all projects";
 * - capped and failed drains show the incomplete banner, a failure keeps
 *   the previous complete graph, and a resync or a late first connect shows
 *   the may-be-stale banner whose Refresh drains again;
 * - the live scope is the dashboard scope throughout;
 * - every interaction issues an exact, counted set of requests, and none
 *   besides the agents list.
 *
 * Shared helpers and hooks for the agent-graph-scope.*.test.ts files.
 */

import { afterEach, beforeAll, beforeEach, expect, vi } from 'vitest';
import type { Agent } from '../../../shared/types.js';
import { stateManager } from '../../../client/state.js';
import { resetHubProjectCapabilitiesCache } from '../../../client/hub-capabilities.js';
import { AgentDrainRunner } from '../../../client/agent-drain.js';
import {
  FakeEventSource,
  fakeFetch,
  jsonResponse,
  makeAgent,
  type Fake,
} from './global-agents-endpoint.js';

export type TestEl = HTMLElement & { updateComplete: Promise<boolean> };

export interface GraphInternals {
  agents: Agent[];
  visibleAgents: Agent[];
  loading: boolean;
  reloading: boolean;
  error: string | null;
  stale: boolean;
  focusId: string;
  orientation: string;
  drainRunner: AgentDrainRunner;
  cappedAll: { members: Map<string, Agent>; loaded: number; stale: boolean } | null;
  memberScope: string | null;
  probed: boolean;
  knownLarge: boolean;
  setProjectFilter(value: string): void;
  onReload(): void;
}

export function g(el: TestEl): GraphInternals {
  return el as unknown as GraphInternals;
}

export const PROBE = '/api/v1/agents?sort=updated&dir=desc&limit=1&fit=500&view=compact';
export const ALL_PAGE = (cursor?: number): string =>
  `/api/v1/agents?limit=500&view=compact${cursor ? `&cursor=${cursor}` : ''}`;
export const PROJECT_PAGE = (id: string, cursor?: number): string =>
  `/api/v1/agents?projectId=${id}&limit=500&view=compact${cursor ? `&cursor=${cursor}` : ''}`;

/** An EventSource that never opens: the live connection never comes up. */
export class SilentEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = SilentEventSource.CONNECTING;
  onopen: ((ev: Event) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  constructor(readonly url: string) {
    super();
  }
  close(): void {
    this.readyState = SilentEventSource.CLOSED;
  }
}

/** A fake whose rows carry `deletion: null`, as the hub's compact rows do when no delete is open. */
export function newFake(count: number): Fake {
  return {
    agents: Array.from({ length: count }, (_, i) => makeAgent(i, { deletion: null })),
    requests: [],
    otherRequests: [],
    projects: [{ id: 'p-1', name: 'P1' }],
  };
}

export function fetchState(): { calls: number; pending: number } {
  const mock = vi.mocked(globalThis.fetch);
  if (!vi.isMockFunction(mock)) return { calls: 0, pending: 0 };
  const settled = mock.mock.settledResults.filter((r) => r.type !== 'incomplete').length;
  return { calls: mock.mock.calls.length, pending: mock.mock.calls.length - settled };
}

/** Waits until the page is idle: no load in flight and every request answered. */
export async function settle(el: TestEl): Promise<void> {
  const idle = (): void => {
    const i = el as unknown as {
      loading?: boolean;
      reloading?: boolean;
      countsLoading?: boolean;
      agentsLoading?: boolean;
    };
    expect(i.loading ?? false).toBe(false);
    expect(i.reloading ?? false).toBe(false);
    expect(i.countsLoading ?? false).toBe(false);
    expect(i.agentsLoading ?? false).toBe(false);
    expect(fetchState().pending).toBe(0);
  };
  for (;;) {
    await vi.waitFor(idle, { timeout: 15_000 });
    const before = fetchState().calls;
    await new Promise((resolve) => setTimeout(resolve, 0));
    (stateManager as unknown as { flush(): void }).flush();
    await el.updateComplete;
    if (fetchState().calls === before) {
      idle();
      return;
    }
  }
}

export const USER = { id: 'u', email: 'u@example.com', name: 'U', role: 'admin' };

/** Mounts the graph at `/agents/graph<search>` without waiting for its load. */
export async function mountUnsettled(
  search = '',
  runner: ConstructorParameters<typeof AgentDrainRunner>[0] = {}
): Promise<TestEl> {
  window.history.replaceState({}, '', `/agents/graph${search}`);
  const el = document.createElement('scion-page-agent-graph') as TestEl;
  (el as unknown as { pageData: unknown }).pageData = {
    path: '/agents/graph',
    title: 'Agent graph',
    user: USER,
  };
  g(el).drainRunner = new AgentDrainRunner({ retryDelayMs: 0, ...runner });
  document.body.appendChild(el);
  await el.updateComplete;
  return el;
}

export async function mountGraph(
  search = '',
  runner: ConstructorParameters<typeof AgentDrainRunner>[0] = {}
): Promise<TestEl> {
  const el = await mountUnsettled(search, runner);
  await settle(el);
  return el;
}

export async function mountPage(tag: 'scion-page-home' | 'scion-page-agents'): Promise<TestEl> {
  window.history.replaceState({}, '', tag === 'scion-page-home' ? '/' : '/agents');
  const el = document.createElement(tag) as TestEl;
  (el as unknown as { pageData: unknown }).pageData = {
    path: tag === 'scion-page-home' ? '/' : '/agents',
    title: 'Page',
    user: USER,
  };
  document.body.appendChild(el);
  await el.updateComplete;
  await settle(el);
  return el;
}

/** Changes the project filter and waits for the page to settle. */
export async function pick(el: TestEl, project: string): Promise<void> {
  g(el).setProjectFilter(project);
  await el.updateComplete;
  await settle(el);
}

export function liveUpdate(subject: string, data: unknown): void {
  (
    stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
  ).handleUpdate({ subject, data });
  (stateManager as unknown as { flush(): void }).flush();
}

export function reconnect(): void {
  stateManager.sseClientInstance.dispatchEvent(new CustomEvent('disconnected'));
  stateManager.sseClientInstance.dispatchEvent(new CustomEvent('connected'));
}

export function ids(agents: Agent[]): string[] {
  return agents.map((a) => a.id).sort();
}

export function projectIds(fake: Fake, projectId: string): string[] {
  return ids(fake.agents.filter((a) => a.projectId === projectId));
}

export function banner(el: TestEl, kind: 'incomplete' | 'stale'): HTMLElement | null {
  return el.shadowRoot?.querySelector(`.graph-${kind}`) ?? null;
}

export function bannerText(el: TestEl, kind: 'incomplete' | 'stale'): string {
  return banner(el, kind)?.querySelector('span')?.textContent?.replace(/\s+/g, ' ').trim() ?? '';
}

export async function clickBanner(el: TestEl, kind: 'incomplete' | 'stale'): Promise<void> {
  (banner(el, kind)?.querySelector('sl-button') as HTMLElement).click();
  await el.updateComplete;
  await settle(el);
}

export function treeNode(el: TestEl, id: string): HTMLElement | null {
  const tree = el.shadowRoot?.querySelector('scion-agent-tree-view');
  return tree?.shadowRoot?.querySelector(`.node[data-agent-id="${id}"]`) ?? null;
}

/** Puts the store in the dashboard scope holding `agents`, optionally marked complete. */
export function holdInState(agents: Agent[], complete: boolean): void {
  stateManager.setScope({ type: 'dashboard' });
  stateManager.seedAgents(agents);
  if (complete) stateManager.markAgentSetComplete('compact');
}

/** Requests since `from`, each of which must ask for the compact view. */
export function graphRequests(fake: Fake, from = 0): string[] {
  const sent = fake.requests.slice(from);
  for (const url of sent) expect(new URL(url, 'http://x').searchParams.get('view')).toBe('compact');
  return sent;
}

/** No request went anywhere but the agents list (no projects, stats or other fetch). */
export function expectOnlyAgentRequests(fake: Fake): void {
  expect(vi.mocked(globalThis.fetch).mock.calls.length).toBe(fake.requests.length);
  expect(fake.otherRequests).toEqual([]);
}

export function rawUrl(input: string | URL | Request): string {
  return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
}

/** `fakeFetch`, except agents requests matching `fails` (decided per request) answer a 500. */
export function failingFetch(fake: Fake, fails: (u: URL) => boolean) {
  const inner = fakeFetch(fake);
  return (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const raw = rawUrl(input);
    const u = new URL(raw, 'http://x');
    if (u.pathname === '/api/v1/agents' && fails(u)) {
      fake.requests.push(raw);
      return Promise.resolve(jsonResponse({ error: { message: 'boom' } }, 500));
    }
    return inner(input, init);
  };
}

/** `fakeFetch`, except the fit probe answers the whole set with no `complete` field (an older server). */
export function legacyProbeFetch(fake: Fake) {
  const inner = fakeFetch(fake);
  return (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const raw = rawUrl(input);
    if (new URL(raw, 'http://x').searchParams.has('sort')) {
      fake.requests.push(raw);
      return Promise.resolve(jsonResponse({ agents: fake.agents }));
    }
    return inner(input, init);
  };
}

export const CAPPED_ALL =
  'Graph incomplete: 2,000 loaded (newest 2,000 checked), more exist · narrow with a project filter';
export const STALE = 'Graph may be stale';

export function treeView(el: TestEl): (HTMLElement & { markMissingAncestors: boolean }) | null {
  return el.shadowRoot?.querySelector('scion-agent-tree-view') ?? null;
}

/** Listeners of `name` on the store added minus removed, since the spies started. */
export function listenerBalance(
  add: ReturnType<typeof vi.spyOn>,
  remove: ReturnType<typeof vi.spyOn>,
  name: string
): number {
  const count = (spy: ReturnType<typeof vi.spyOn>): number =>
    spy.mock.calls.filter(([type]) => type === name).length;
  return count(add) - count(remove);
}

/**
 * Gives each `stateManager.sseConnected` call its own promise, resolved by
 * the test, so one attachment's connect wait can end without another's.
 */
export function deferredConnects(): Array<{ promise: Promise<void>; resolve: () => void }> {
  const waits: Array<{ promise: Promise<void>; resolve: () => void }> = [];
  vi.spyOn(stateManager, 'sseConnected').mockImplementation(() => {
    let resolve!: () => void;
    const promise = new Promise<void>((r) => {
      resolve = r;
    });
    waits.push({ promise, resolve });
    return promise;
  });
  return waits;
}

/** The agents page's own first load in the list view. */
export const AGENTS_PAGE_LOAD = '/api/v1/agents?sort=updated&dir=desc&limit=25&fit=500&stats=1';

/** Set by useGraphScopeHooks() before each test; test files read it through the live binding. */
export let setScopeSpy: ReturnType<typeof vi.spyOn>;

/**
 * Registers the suite's hooks; call it first inside the outer describe.
 * Keeps the page in the dashboard scope (see the afterEach) and sets
 * `setScopeSpy` for each test.
 */
export function useGraphScopeHooks(): void {
  beforeAll(async () => {
    await import('../agent-graph.js');
    await import('../home.js');
    await import('../agents.js');
  }, 60_000);

  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeEventSource);
    // A real scope change clears the store and its completeness flag.
    stateManager.setScope({ type: 'brokers-list' });
    resetHubProjectCapabilitiesCache();
    localStorage.clear();
    setScopeSpy = vi.spyOn(stateManager, 'setScope');
  });

  afterEach(() => {
    // The page never leaves the dashboard scope (a project scope would
    // clear the complete set later picker changes and pages reuse).
    for (const [scope] of setScopeSpy.mock.calls) {
      expect((scope as { type: string }).type).not.toBe('project');
    }
    setScopeSpy.mockRestore();
    document.body
      .querySelectorAll('scion-page-agent-graph, scion-page-home, scion-page-agents')
      .forEach((n) => n.remove());
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    localStorage.clear();
    window.history.replaceState({}, '', '/');
  });
}

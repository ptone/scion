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
 * The global agents page through the agent list window: the sorted fit
 * request, the reuse branch and the completeness flag it sets, the
 * complete-needing views (tree, mode filter), refresh costs per window
 * state, the error path, the empty states, and count-only mode above
 * 2,000 agents. Uses the real `stateManager`; only `fetch`, `EventSource`
 * and `localStorage` are faked.
 *
 * Shared helpers and hooks for the agents-agent-window.*.test.ts files.
 */

import { expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import type { Agent } from '../../../shared/types.js';
import { stateManager } from '../../../client/state.js';
import type { AgentListWindow } from '../../../client/agent-list-window.js';
import { AgentDrainRunner } from '../../../client/agent-drain.js';
import { FakeEventSource, fakeFetch, type Fake } from './global-agents-endpoint.js';

export interface Internals {
  agentWindow: AgentListWindow;
  agents: Agent[];
  error: string | null;
  loading: boolean;
  agentsLoading: boolean;
  committedLabel: string;
  toggleSort(field: string): void;
  setPhaseFilter(phase: string): void;
  setModeFilter(mode: string): void;
  setScope(scope: 'all' | 'mine' | 'shared'): void;
  backgroundRefresh(trigger: string): void;
  handleStopAll(): Promise<void>;
  handleAgentAction(id: string, action: string, event?: MouseEvent): Promise<void>;
}

export type TestEl = HTMLElement & { updateComplete: Promise<boolean> };

export function internals(el: TestEl): Internals {
  return el as unknown as Internals;
}

/**
 * Waits until the page is idle (no page load, no agents load, no window
 * page fetch), runs any pending live flush, re-renders, and checks the page
 * is still idle, so a zero-request assertion never races a late request.
 */
export async function settle(el: TestEl): Promise<void> {
  const idle = (): void => {
    expect(internals(el).loading).toBe(false);
    expect(internals(el).agentsLoading).toBe(false);
    expect(internals(el).agentWindow.loading).toBe(false);
  };
  await vi.waitFor(idle, { timeout: 10_000 });
  (stateManager as unknown as { flush(): void }).flush();
  await el.updateComplete;
  await vi.waitFor(idle, { timeout: 10_000 });
}

/** Mounts the page and returns once its first request is sent, without waiting for it. */
export async function mountUnsettled(): Promise<TestEl> {
  const el = document.createElement('scion-page-agents') as TestEl;
  (el as unknown as { pageData: unknown }).pageData = {
    path: '/agents',
    title: 'Agents',
    user: { id: 'u', email: 'u@example.com', name: 'U', role: 'member' },
  };
  // Drain retries without a delay.
  (el as unknown as { drainRunner: AgentDrainRunner }).drainRunner = new AgentDrainRunner({
    retryDelayMs: 0,
  });
  document.body.appendChild(el);
  await el.updateComplete;
  return el;
}

export async function mount(): Promise<TestEl> {
  const el = await mountUnsettled();
  await settle(el);
  return el;
}

/** The query of a request URL. */
export function query(url: string): URLSearchParams {
  return new URL(url, 'http://x').searchParams;
}

export function unmount(el: TestEl): void {
  el.remove();
}

export function text(el: TestEl): string {
  return el.shadowRoot?.textContent?.replace(/\s+/g, ' ') ?? '';
}

export function labelInput(el: TestEl): HTMLElement & { value: string } {
  const inputs = Array.from(el.shadowRoot?.querySelectorAll('sl-input') ?? []);
  return inputs.find(
    (i) => i.getAttribute('placeholder') === 'Filter by label (key=value)'
  ) as HTMLElement & { value: string };
}

export function typeLabel(el: TestEl, value: string): void {
  const input = labelInput(el);
  input.value = value;
  input.dispatchEvent(new Event('sl-input'));
}

export function commitLabel(el: TestEl, value: string): void {
  typeLabel(el, value);
  labelInput(el).dispatchEvent(new Event('sl-change'));
}

export function setView(el: TestEl, view: string): void {
  el.shadowRoot
    ?.querySelector('scion-view-toggle')
    ?.dispatchEvent(new CustomEvent('view-change', { detail: { view } }));
}

export function pager(el: TestEl): HTMLElement & {
  showChip: boolean;
  chipText: string;
  updateComplete: Promise<unknown>;
} {
  return el.shadowRoot?.querySelector('scion-agent-pager') as HTMLElement & {
    showChip: boolean;
    chipText: string;
    updateComplete: Promise<unknown>;
  };
}

export function handleUpdate(subject: string, data: unknown): void {
  (
    stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
  ).handleUpdate({ subject, data });
}

/** Runs state.ts's pending coalesced flush now, then lets the page re-render. */
export async function flushLive(el: TestEl): Promise<void> {
  (stateManager as unknown as { flush(): void }).flush();
  await Promise.resolve();
  await el.updateComplete;
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

/**
 * Drops and reopens the live connection through the state store's own
 * SSE client, so the resync goes through the real state path.
 */
export function reconnect(): void {
  const sse = stateManager.sseClientInstance;
  sse.dispatchEvent(new CustomEvent('disconnected'));
  sse.dispatchEvent(new CustomEvent('connected'));
}

/**
 * Fetch fakes are stubbed as plain functions, not vi.fn(): Vitest keeps
 * every vi.fn() and its implementation until the file ends, and a fake's
 * recorded requests hold abort signals that reach the page that sent
 * them, so each test's page and agents would stay on the heap.
 */
export function stubFake(fake: Fake): void {
  vi.stubGlobal('fetch', fakeFetch(fake));
}

export function hasStopAll(el: TestEl): boolean {
  return Array.from(el.shadowRoot?.querySelectorAll('sl-button') ?? []).some((b) =>
    b.textContent?.includes('Stop All')
  );
}

/** Registers the suite's hooks; call it first inside the outer describe. */
export function useAgentsWindowHooks(): void {
  beforeAll(async () => {
    await import('../agents.js');
  }, 60_000);

  beforeEach(() => {
    vi.stubGlobal('EventSource', FakeEventSource);
    // A real scope change clears the state store and the completeness flag.
    stateManager.setScope({ type: 'brokers-list' });
    localStorage.clear();
    localStorage.setItem('scion-view-agents', 'list');
  });

  afterEach(() => {
    document.body.querySelectorAll('scion-page-agents').forEach((n) => n.remove());
    // Vitest keeps every vi.fn() and spy, with its recorded calls, until
    // the file ends. Clear the calls so they do not keep removed pages and
    // their agents reachable.
    vi.clearAllMocks();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    localStorage.clear();
  });
}

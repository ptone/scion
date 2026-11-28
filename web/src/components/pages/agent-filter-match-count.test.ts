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

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { resetServerFlagStateForTests } from '../../utils/feature-flags.js';
import { stateManager } from '../../client/state.js';
import type { Agent, Project } from '../../shared/types.js';
import type { ScionFilterBox } from '../shared/filter-box/filter-box.js';
import './agents.js';
import type { ScionPageAgents } from './agents.js';
import './project-detail.js';
import type { ScionPageProjectDetail } from './project-detail.js';
import './agent-graph.js';
import type { AgentGraphPage } from './agent-graph.js';

function mkAgent(overrides: Partial<Agent> & { id: string }): Agent {
  return {
    name: `Agent ${overrides.id}`,
    slug: overrides.id,
    projectId: 'proj-1',
    project: 'Alpha',
    projectSlug: 'alpha',
    template: 'developer',
    phase: 'running',
    ...overrides,
  };
}

const eventSources: FakeEventSource[] = [];

class FakeEventSource extends EventTarget {
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  constructor() {
    super();
    eventSources.push(this);
    queueMicrotask(() => {
      this.onopen?.();
      this.dispatchEvent(new Event('open'));
    });
  }
  emitUpdate(subject: string, data: Record<string, unknown>): void {
    this.dispatchEvent(new MessageEvent('update', { data: JSON.stringify({ subject, data }) }));
  }
  close(): void {}
}

function stubEventSource(): void {
  eventSources.length = 0;
  vi.stubGlobal('EventSource', FakeEventSource);
}

async function flushRender(el: { updateComplete: Promise<boolean> }): Promise<void> {
  await new Promise((r) => setTimeout(r, 20));
  await el.updateComplete;
  await new Promise((r) => setTimeout(r, 10));
  await el.updateComplete;
}

function readResultCount(host: HTMLElement): string {
  const box = host.shadowRoot?.querySelector('scion-filter-box') as ScionFilterBox | null;
  if (!box) return '';
  return box.shadowRoot?.querySelector('.result-count')?.textContent?.trim() ?? '';
}

function typeFilter(host: HTMLElement, value: string, commit: 'none' | 'enter' | 'blur'): void {
  const box = host.shadowRoot?.querySelector('scion-filter-box') as ScionFilterBox;
  const input = box.shadowRoot?.querySelector('input') as HTMLInputElement;
  input.focus();
  input.value = value;
  input.dispatchEvent(new Event('input'));
  if (commit === 'enter') {
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
  } else if (commit === 'blur') {
    input.dispatchEvent(new FocusEvent('blur', { relatedTarget: document.body }));
  }
}

describe('Filter match count across agent views (ptone/scion#9926)', () => {
  beforeEach(() => {
    localStorage.clear();
    window.history.replaceState(null, '', '/agents');
    resetServerFlagStateForTests();
    stubEventSource();
    // Force a scope transition so stateManager clears agents from the previous test.
    stateManager.setScope({ type: 'agent-detail', agentId: '__test_reset__' });
    stateManager.setScope({ type: 'dashboard' });
  });

  afterEach(() => {
    document.body.innerHTML = '';
    vi.unstubAllGlobals();
    localStorage.clear();
    window.history.replaceState(null, '', '/agents');
    resetServerFlagStateForTests();
  });

  it('<scion-page-agents> hides count when unfiltered, updates on selector clicks and query commit (not keystroke), and handles zero matches', async () => {
    const agents: Agent[] = [
      mkAgent({ id: 'a1', name: 'Alpha One', phase: 'running', harnessConfig: 'claude' }),
      mkAgent({ id: 'a2', name: 'Alpha Two', phase: 'running', harnessConfig: 'gemini' }),
      mkAgent({ id: 'a3', name: 'Beta One', phase: 'stopped', harnessConfig: 'claude' }),
      mkAgent({ id: 'a4', name: 'Gamma One', phase: 'error', harnessConfig: 'codex' }),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ agents, complete: true, totalCount: agents.length }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        )
      )
    );

    const el = document.createElement('scion-page-agents') as ScionPageAgents;
    document.body.appendChild(el);
    await flushRender(el);

    // Unfiltered: count is hidden.
    expect(readResultCount(el)).toBe('');

    // Selector click ("Running") updates count immediately.
    const buttons = Array.from(el.shadowRoot?.querySelectorAll('.scope-toggle button') ?? []);
    const runningBtn = buttons.find(
      (b) => b.textContent?.trim() === 'Running'
    ) as HTMLButtonElement;
    const suspendedBtn = buttons.find(
      (b) => b.textContent?.trim() === 'Suspended'
    ) as HTMLButtonElement;
    const allBtn = buttons.find((b) => b.textContent?.trim() === 'All') as HTMLButtonElement;

    runningBtn.click();
    await flushRender(el);
    expect(readResultCount(el)).toBe('2 of 4 agents');

    // Zero matches via selector.
    suspendedBtn.click();
    await flushRender(el);
    expect(readResultCount(el)).toBe('0 of 4 agents');

    // Clearing back to All hides count.
    allBtn.click();
    await flushRender(el);
    expect(readResultCount(el)).toBe('');

    // Typing a query without committing does NOT show or change the count.
    typeFilter(el, 'alpha', 'none');
    await flushRender(el);
    expect(readResultCount(el)).toBe('');

    // Pressing Enter commits and updates the count.
    typeFilter(el, 'alpha', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('2 of 4 agents');

    // Live SSE update: deleting a matching agent updates both matched and total.
    eventSources[eventSources.length - 1]?.emitUpdate('project.proj-1.agent.deleted', {
      agentId: 'a1',
      projectId: 'proj-1',
    });
    await flushRender(el);
    expect(readResultCount(el)).toBe('1 of 3 agents');
  });

  it('<scion-page-agents> preserves unfiltered total when a committed phase query returns 0 rows from the server', async () => {
    const agents: Agent[] = [
      mkAgent({ id: 'a1', phase: 'running' }),
      mkAgent({ id: 'a2', phase: 'stopped' }),
      mkAgent({ id: 'a3', phase: 'running' }),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
        if (url.includes('q=phase%3Aerror')) {
          return Promise.resolve(
            new Response(JSON.stringify({ agents: [], complete: true, totalCount: 0 }), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            })
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({ agents, complete: true, totalCount: agents.length }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        );
      })
    );

    const el = document.createElement('scion-page-agents') as ScionPageAgents;
    document.body.appendChild(el);
    await flushRender(el);

    typeFilter(el, 'phase:error', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('0 of 3 agents');
  });

  it('<scion-page-agents> uses server totalCount when paged instead of loaded page length', async () => {
    const pageAgents: Agent[] = [
      mkAgent({ id: 'a1', phase: 'running' }),
      mkAgent({ id: 'a2', phase: 'running' }),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
        if (url.includes('q=phase%3Arunning')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                agents: pageAgents,
                complete: false,
                nextCursor: 'cursor-2',
                totalCount: 12,
                stats: { total: 84, running: 12 },
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } }
            )
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              agents: pageAgents,
              complete: false,
              nextCursor: 'cursor-1',
              totalCount: 84,
              stats: { total: 84, running: 12 },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
        );
      })
    );

    const el = document.createElement('scion-page-agents') as ScionPageAgents;
    document.body.appendChild(el);
    await flushRender(el);
    expect(readResultCount(el)).toBe('');

    // Commit phase:running -> paged response has only 2 rows on page, server totalCount is 12 of 84.
    typeFilter(el, 'phase:running', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('12 of 84 agents');
  });

  it('<scion-page-agents> shows "N matching" on deep-linked server query when unfiltered total is unknown, and "+" when approximate', async () => {
    window.history.replaceState(null, '', '/agents?q=label%3Aenv%3Dprod');
    const pageAgents: Agent[] = [mkAgent({ id: 'a1', labels: { env: 'prod' } })];

    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              agents: pageAgents,
              complete: false,
              nextCursor: 'c2',
              totalCount: 50,
              totalCountApproximate: true,
              stats: { total: 50, running: 10, totalApproximate: true },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
        )
      )
    );

    const el = document.createElement('scion-page-agents') as ScionPageAgents;
    document.body.appendChild(el);
    await flushRender(el);
    expect(readResultCount(el)).toBe('50+ matching');
  });

  it('<scion-page-project-detail> shows project-scoped match count for local and paged results', async () => {
    window.history.replaceState(null, '', '/projects/proj-1');
    const project: Project = {
      id: 'proj-1',
      name: 'Alpha Project',
      slug: 'alpha',
      agentCount: 42,
      created: '2026-01-01T00:00:00Z',
      updated: '2026-01-01T00:00:00Z',
    };
    const pageAgents: Agent[] = [
      mkAgent({ id: 'p-a1', phase: 'running', projectId: 'proj-1' }),
      mkAgent({ id: 'p-a2', phase: 'stopped', projectId: 'proj-1' }),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
        if (url.endsWith('/api/v1/projects/proj-1')) {
          return Promise.resolve(
            new Response(JSON.stringify(project), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            })
          );
        }
        if (url.includes('/api/v1/projects/proj-1/agents')) {
          if (url.includes('q=phase%3Arunning')) {
            return Promise.resolve(
              new Response(
                JSON.stringify({
                  agents: [pageAgents[0]],
                  complete: false,
                  nextCursor: 'next-1',
                  totalCount: 9,
                  stats: { total: 42, running: 9 },
                }),
                { status: 200, headers: { 'Content-Type': 'application/json' } }
              )
            );
          }
          return Promise.resolve(
            new Response(
              JSON.stringify({
                agents: pageAgents,
                complete: false,
                nextCursor: 'next-0',
                totalCount: 42,
                stats: { total: 42, running: 9 },
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } }
            )
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({}), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        );
      })
    );

    const el = document.createElement('scion-page-project-detail') as ScionPageProjectDetail;
    el.projectId = 'proj-1';
    document.body.appendChild(el);
    await flushRender(el);

    expect(readResultCount(el)).toBe('');

    const buttons = Array.from(el.shadowRoot?.querySelectorAll('.scope-toggle button') ?? []);
    const runningBtn = buttons.find(
      (b) => b.textContent?.trim() === 'Running'
    ) as HTMLButtonElement;
    runningBtn.click();
    await flushRender(el);
    expect(readResultCount(el)).toBe('9 of 42 agents');
  });

  it('<scion-page-agent-graph> counts directly matching agents (excluding lineage context ancestors) and updates on blur/Enter', async () => {
    window.history.replaceState(null, '', '/agents/graph');
    const agents: Agent[] = [
      mkAgent({ id: 'root', name: 'Coordinator', phase: 'stopped' }),
      mkAgent({
        id: 'mid',
        name: 'Lead',
        phase: 'suspended',
        ancestry: ['user:u1', 'root'],
      }),
      mkAgent({
        id: 'leaf',
        name: 'Worker',
        phase: 'running',
        ancestry: ['user:u1', 'root', 'mid'],
      }),
      mkAgent({ id: 'other', name: 'Other', phase: 'stopped' }),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ agents, complete: true }), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        )
      )
    );

    const el = document.createElement('scion-page-agent-graph') as AgentGraphPage;
    document.body.appendChild(el);
    await flushRender(el);

    // Unfiltered: hidden.
    expect(readResultCount(el)).toBe('');

    // Typing without commit does not show count.
    typeFilter(el, 'phase:running', 'none');
    await flushRender(el);
    expect(readResultCount(el)).toBe('');

    // Commit on blur: only 'leaf' matches (root and mid are context ancestors, NOT counted as matches).
    typeFilter(el, 'phase:running', 'blur');
    await flushRender(el);
    expect(readResultCount(el)).toBe('1 of 4 agents');

    // Commit zero-match query on Enter.
    typeFilter(el, 'phase:error', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('0 of 4 agents');
  });

  it('<scion-page-agents> deep-linked phase:running served as a complete fit set shows "16 matching", never "16 of 16 agents"', async () => {
    window.history.replaceState(null, '', '/agents?q=phase%3Arunning');
    const runningAgents = Array.from({ length: 16 }, (_, i) =>
      mkAgent({ id: `run-${i + 1}`, phase: 'running' })
    );

    vi.stubGlobal(
      'fetch',
      vi.fn(() =>
        Promise.resolve(
          new Response(
            JSON.stringify({
              agents: runningAgents,
              complete: true,
              totalCount: runningAgents.length,
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
        )
      )
    );

    const el = document.createElement('scion-page-agents') as ScionPageAgents;
    document.body.appendChild(el);
    await flushRender(el);

    expect(readResultCount(el)).toBe('16 matching');
  });

  it('<scion-page-agents> unfiltered paged load (1,200) followed by phase:running complete fit (480) shows "480 of 1,200 agents", never store size', async () => {
    const runningAgents = Array.from({ length: 480 }, (_, i) =>
      mkAgent({ id: `run-${i + 1}`, phase: 'running' })
    );
    const otherAgents = Array.from({ length: 15 }, (_, i) =>
      mkAgent({ id: `stop-${i + 1}`, phase: 'stopped' })
    );
    // Initial paged response seeds 15 non-running + 10 running into stateManager;
    // then the complete fit response for phase:running seeds 480 running -> stateManager has 495.
    const firstPage = [...otherAgents, ...runningAgents.slice(0, 10)];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
        if (url.includes('q=phase%3Arunning')) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                agents: runningAgents,
                complete: true,
                totalCount: runningAgents.length,
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } }
            )
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              agents: firstPage,
              complete: false,
              nextCursor: 'cursor-1',
              totalCount: 1200,
              stats: { total: 1200, running: 480 },
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
        );
      })
    );

    const el = document.createElement('scion-page-agents') as ScionPageAgents;
    document.body.appendChild(el);
    await flushRender(el);
    expect(readResultCount(el)).toBe('');

    typeFilter(el, 'phase:running', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('480 of 1,200 agents');
  });

  it('<scion-page-project-detail> honors totalCountApproximate on paged unfiltered and filtered loads', async () => {
    window.history.replaceState(null, '', '/projects/proj-1');
    const project: Project = {
      id: 'proj-1',
      name: 'Alpha Project',
      slug: 'alpha',
      agentCount: 500,
      created: '2026-01-01T00:00:00Z',
      updated: '2026-01-01T00:00:00Z',
    };
    const pageAgents: Agent[] = [
      mkAgent({ id: 'p-a1', phase: 'running', projectId: 'proj-1', labels: { env: 'prod' } }),
      mkAgent({ id: 'p-a2', phase: 'stopped', projectId: 'proj-1' }),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
        if (url.endsWith('/api/v1/projects/proj-1')) {
          return Promise.resolve(
            new Response(JSON.stringify(project), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            })
          );
        }
        if (url.includes('/api/v1/projects/proj-1/agents')) {
          if (url.includes('q=label%3Aenv%3Dprod')) {
            return Promise.resolve(
              new Response(
                JSON.stringify({
                  agents: [pageAgents[0]],
                  complete: false,
                  nextCursor: 'next-1',
                  totalCount: 25,
                  totalCountApproximate: true,
                  stats: { total: 25, running: 10 },
                }),
                { status: 200, headers: { 'Content-Type': 'application/json' } }
              )
            );
          }
          return Promise.resolve(
            new Response(
              JSON.stringify({
                agents: pageAgents,
                complete: false,
                nextCursor: 'next-0',
                totalCount: 500,
                totalCountApproximate: true,
                stats: { total: 500, running: 100 },
              }),
              { status: 200, headers: { 'Content-Type': 'application/json' } }
            )
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({}), {
            status: 200,
            headers: { 'Content-Type': 'application/json' },
          })
        );
      })
    );

    const el = document.createElement('scion-page-project-detail') as ScionPageProjectDetail;
    el.projectId = 'proj-1';
    document.body.appendChild(el);
    await flushRender(el);

    typeFilter(el, 'label:env=prod', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('25+ of 500+ agents');
  });

  it('<scion-page-agent-graph> uses project-scoped denominator after narrowing to a project on a large fleet', async () => {
    window.history.replaceState(null, '', '/agents/graph');
    // Unscoped initial drain returns 2 pages hitting a partial/capped state or non-complete set,
    // then narrowing with `project:alpha phase:running` drains project `proj-1` (10 agents, 3 running).
    const p1Agents: Agent[] = Array.from({ length: 10 }, (_, i) =>
      mkAgent({
        id: `p1-${i + 1}`,
        projectId: 'proj-1',
        project: 'Alpha',
        projectSlug: 'alpha',
        phase: i < 3 ? 'running' : 'stopped',
      })
    );
    const globalPage: Agent[] = [
      ...p1Agents,
      ...Array.from({ length: 40 }, (_, i) =>
        mkAgent({
          id: `p2-${i + 1}`,
          projectId: 'proj-2',
          project: 'Beta',
          projectSlug: 'beta',
          phase: 'stopped',
        })
      ),
    ];

    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url;
        if (url.includes('projectId=')) {
          return Promise.resolve(
            new Response(JSON.stringify({ agents: p1Agents, complete: true }), {
              status: 200,
              headers: { 'Content-Type': 'application/json' },
            })
          );
        }
        if (url.includes('cursor=')) {
          // Simulate drain failure after first page so store is NOT marked complete,
          // while global set has 50 agents.
          return Promise.resolve(new Response('boom', { status: 500 }));
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ agents: globalPage, complete: false, nextCursor: 'page-2' }),
            { status: 200, headers: { 'Content-Type': 'application/json' } }
          )
        );
      })
    );

    const el = document.createElement('scion-page-agent-graph') as AgentGraphPage;
    document.body.appendChild(el);
    await flushRender(el);

    typeFilter(el, 'project:alpha phase:running', 'enter');
    await flushRender(el);
    expect(readResultCount(el)).toBe('3 of 10 agents');
  });
});

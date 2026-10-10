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
 * Fit-threshold paging, live updates, reconnects, and request counts per agent count.
 *
 * One part of the "project-detail — agent list window" suite. The suite is split
 * across project-detail-agent-window.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/project-detail-agent-window.ts.
 */

import { describe, it, expect, vi } from 'vitest';
import type { Agent } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import { PROJECT_AGENTS_FIT_THRESHOLD } from '../../client/agent-list-window.js';
import { holdable } from './__fixtures__/global-agents-endpoint.js';
import {
  jsonResponse,
  stubSortedAgentsWithoutList,
  makeAgent,
  type AgentsRequest,
  createFetchHandler,
  type TestEl,
  sentUrls,
  stubFetch,
  createComponent,
  labelInput,
  viewToggle,
  internals,
  flushLive,
  settle,
  deferred,
  createRealisticFetchHandler,
  deferLiveFlush,
  watchDeleteFlush,
  useProjectDetailWindowHooks,
} from './__fixtures__/project-detail-agent-window.js';

// Phase 2 (ptone/scion#2483): the shared delete helper, spied but running
// for real (pass-through), so tests can prove the page delegates to it; the
// confirm dialog and toasts are stubbed (they need real Shoelace elements).
vi.mock('../../client/agent-delete.js', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../client/agent-delete.js')>();
  return { ...actual, runAgentDelete: vi.fn(actual.runAgentDelete) };
});
vi.mock('../shared/confirm-dialog.js', () => ({
  showConfirm: vi.fn(() => Promise.resolve(true)),
}));
vi.mock('../../utils/toast.js', () => ({ showToast: vi.fn() }));
// (Stop All also asks for confirmation first; this mock confirms it.)

// Mounting a 100-agent grid/list page is slow under the default 5s per-test
// timeout on a loaded machine; these tests do real work (fetch handling,
// multiple Lit render passes) rather than looping. The timeout is the third
// describe argument: a test's timeout is fixed when it is registered, so
// vi.setConfig inside beforeEach (which runs later) had no effect.
describe('project-detail — agent list window', () => {
  useProjectDetailWindowHooks();

  it('shows a provision-only agent as created (not started) in list and grid (ptone/scion#2929)', async () => {
    const projectId = 'p-provisioned';
    localStorage.setItem('scion-view-project-agents', 'list');
    const agents = [
      makeAgent(1, { projectId, phase: 'created', provisionedOnly: true }),
      makeAgent(2, { projectId, phase: 'created' }),
    ];
    const requests: AgentsRequest[] = [];
    stubFetch(
      createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
    );
    const el = await createComponent(projectId);
    const badges = () =>
      Array.from(el.shadowRoot?.querySelectorAll('scion-status-badge') ?? []).map((b) => [
        b.getAttribute('label'),
        b.getAttribute('title'),
      ]);
    const expected = [
      ['created (not started)', 'Not started yet. Use Start, or run: scion start agent-1'],
      ['created', null],
    ];
    expect(badges().sort()).toEqual(expected);

    viewToggle(el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'grid' } }));
    await el.updateComplete;
    expect(badges().sort()).toEqual(expected);
  });

  describe('sorted/paged mode — list view, updated sort, at the fit threshold', () => {
    it('page load issues exactly one agents request (the fit request); every client-only interaction issues zero; label commit and lifecycle refresh issue exactly one each', async () => {
      const projectId = 'p-w10-list';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: PROJECT_AGENTS_FIT_THRESHOLD }, (_, i) =>
        makeAgent(i, { projectId })
      );
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect(requests.length).toBe(1);
      expect(requests[0].url).toContain('sort=updated');
      expect(requests[0].url).toContain(`fit=${PROJECT_AGENTS_FIT_THRESHOLD}`);
      expect(internals(el).agentWindow.state).toBe('small');

      const toggle = viewToggle(el)!;

      // list -> grid -> list: 0 requests (complete set already held).
      toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'grid' } }));
      await el.updateComplete;
      expect(requests.length).toBe(1);
      toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'list' } }));
      await el.updateComplete;
      expect(requests.length).toBe(1);

      // list -> tree ('graph' view mode): 0 requests.
      toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'graph' } }));
      await el.updateComplete;
      expect(requests.length).toBe(1);
      toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'list' } }));
      await el.updateComplete;
      expect(requests.length).toBe(1);

      // Sort dir flip, sort -> name, phase change: 0 requests (small state is local).
      internals(el).toggleSort('updated'); // flips dir
      expect(requests.length).toBe(1);
      internals(el).toggleSort('name');
      expect(requests.length).toBe(1);
      internals(el).toggleSort('updated'); // back to updated for the page-nav check below
      internals(el).setPhaseFilter('running');
      internals(el).setPhaseFilter('');
      expect(requests.length).toBe(1);

      // Page navigation: 0 requests (local slice of the held set).
      await internals(el).agentWindow.next();
      await internals(el).agentWindow.prev();
      expect(requests.length).toBe(1);

      // Label typing: 0 requests per keystroke.
      const input = labelInput(el)!;
      input.value = 'e';
      input.dispatchEvent(new Event('sl-input'));
      input.value = 'env';
      input.dispatchEvent(new Event('sl-input'));
      expect(requests.length).toBe(1);

      // Label commit (sl-change): exactly one request.
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      expect(requests.length).toBe(2);

      // Lifecycle refresh: exactly one request.
      await internals(el).handleAgentAction('a-1', 'stop');
      await settle(el);
      expect(requests.length).toBe(3);
    }, 20_000);

    it('the same page opened in grid view issues exactly one agents request (the fit request), and grid -> list issues zero', async () => {
      // Grid with the updated sort is sorted-eligible, the same as list.
      const projectId = 'p-w10-grid';
      localStorage.setItem('scion-view-project-agents', 'grid');
      const agents = Array.from({ length: 100 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect(requests.length).toBe(1);
      expect(requests[0].url).toContain('sort=updated');

      const toggle = viewToggle(el)!;
      toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'list' } }));
      await el.updateComplete;
      expect(requests.length).toBe(1);
    });
  });

  describe('above the fit threshold the list view pages from the first request', () => {
    const mountList = async (projectId: string, count: number, pageSize?: number) => {
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      if (pageSize !== undefined) {
        localStorage.setItem('scion-pagesize-project-agents', String(pageSize));
      }
      // Every agent carries env=prod, so committing that label keeps the
      // candidate set above the threshold and the window stays paged.
      const agents = Array.from({ length: count }, (_, i) =>
        makeAgent(i, { projectId, labels: { env: 'prod' } })
      );
      const requests: AgentsRequest[] = [];
      stubFetch(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })
      );
      const el = await createComponent(projectId);
      return { el, requests };
    };

    it('one agent above the threshold: the first request carries the threshold as fit and the window is paged', async () => {
      const { el, requests } = await mountList('p-fit-boundary', PROJECT_AGENTS_FIT_THRESHOLD + 1);
      expect(requests.length).toBe(1);
      expect(requests[0].url).toContain(`fit=${PROJECT_AGENTS_FIT_THRESHOLD}`);
      expect(requests[0].url).toContain('limit=25');
      expect(internals(el).agentWindow.state).toBe('paged');
    });

    it('100 agents: one request on load; each paged interaction costs exactly one; list <-> grid is free while paged; tree drains once, after which toggles are free', async () => {
      const { el, requests } = await mountList('p-fit-100', 100);
      expect(requests.length).toBe(1);
      expect(requests[0].url).toContain(`fit=${PROJECT_AGENTS_FIT_THRESHOLD}`);
      expect(internals(el).agentWindow.state).toBe('paged');

      let n = requests.length;
      await internals(el).agentWindow.next();
      expect(requests.length - n).toBe(1);
      // Next asks for the next slice of the walk order frozen from page 0.
      expect(requests[requests.length - 1].url).toContain('ids=');
      expect(requests[requests.length - 1].url).not.toContain('cursor=');

      n = requests.length;
      await internals(el).agentWindow.prev();
      expect(requests.length - n).toBe(1);

      n = requests.length;
      internals(el).toggleSort('updated'); // flips dir
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain('dir=asc');
      expect(internals(el).agentWindow.state).toBe('paged');

      n = requests.length;
      internals(el).setPhaseFilter('running');
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain('phase=running');
      n = requests.length;
      internals(el).setPhaseFilter('');
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(internals(el).agentWindow.state).toBe('paged');

      // Label typing is free; the commit is one request.
      const input = labelInput(el)!;
      n = requests.length;
      input.value = 'env';
      input.dispatchEvent(new Event('sl-input'));
      expect(requests.length - n).toBe(0);
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain('label=env%3Dprod');
      expect(internals(el).agentWindow.state).toBe('paged');

      n = requests.length;
      await internals(el).handleAgentAction('a-1', 'stop');
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain(`fit=${PROJECT_AGENTS_FIT_THRESHOLD}`);
      expect(internals(el).agentWindow.state).toBe('paged');

      // Clear the label so the grid load is unfiltered.
      n = requests.length;
      input.value = '';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(internals(el).agentWindow.state).toBe('paged');

      // list <-> grid while paged: both render the same server page.
      const toggle = viewToggle(el)!;
      n = requests.length;
      for (const v of ['grid', 'list', 'grid']) {
        toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: v } }));
        await settle(el);
      }
      expect(requests.length - n).toBe(0);
      expect(internals(el).agentWindow.state).toBe('paged');
      await el.updateComplete;
      expect(el.shadowRoot!.querySelectorAll('.agent-grid > *').length).toBe(25);

      // grid -> tree: one drain (a single legacy page at 100 agents), then
      // every toggle is free.
      n = requests.length;
      toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'graph' } }));
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).not.toContain('sort=');
      expect(internals(el).agentWindow.state).toBe('small');

      n = requests.length;
      for (const v of ['list', 'grid', 'graph', 'list']) {
        toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: v } }));
        await settle(el);
      }
      expect(requests.length - n).toBe(0);
      expect(internals(el).agentWindow.state).toBe('small');
    }, 20_000);

    it('page size 100: fit is raised to the page size, so 100 agents is small and 101 is paged', async () => {
      const small = await mountList('p-fit-ps100-small', 100, 100);
      expect(small.requests.length).toBe(1);
      expect(small.requests[0].url).toContain('fit=100');
      expect(small.requests[0].url).toContain('limit=100');
      expect(internals(small.el).agentWindow.state).toBe('small');
      small.el.remove();
      localStorage.clear();

      const paged = await mountList('p-fit-ps100-paged', 101, 100);
      expect(paged.requests.length).toBe(1);
      expect(paged.requests[0].url).toContain('fit=100');
      expect(internals(paged.el).agentWindow.state).toBe('paged');
    });

    it('a page-size change while paged sends fit raised to the new page size, never below limit', async () => {
      const { el, requests } = await mountList('p-fit-ps-change', 100);
      expect(internals(el).agentWindow.state).toBe('paged');

      const n = requests.length;
      internals(el).onPagerSizeChange(100);
      await settle(el);
      expect(requests.length - n).toBe(1);
      const url = requests[requests.length - 1].url;
      expect(url).toContain('limit=100');
      expect(url).toContain('fit=100');
      expect(internals(el).agentWindow.state).toBe('small');
    });
  });

  describe('422 fallback and per-label memory', () => {
    it('a 422 falls back to the legacy load and is remembered for that label; a new label retries once', async () => {
      const projectId = 'p-422';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 10 }, (_, i) =>
        makeAgent(i, { projectId, labels: { env: 'prod' } })
      );
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
          refuseSortedForLabel: '', // the initial unlabelled load is refused
        })
      );

      const el = await createComponent(projectId);
      // Page load: sorted request refused (422), falls back to the legacy load.
      expect(requests.length).toBe(2);
      expect(requests[0].url).toContain('sort=updated');
      expect(requests[1].url).not.toContain('sort=');

      // A lifecycle refresh with the SAME (still refused) label does not retry sorted mode.
      await internals(el).handleAgentAction('a-1', 'stop');
      await settle(el);
      expect(requests.length).toBe(3);
      expect(requests[2].url).not.toContain('sort=');

      // A new label commit retries sorted mode once.
      const input = labelInput(el)!;
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      expect(requests.length).toBe(4);
      expect(requests[3].url).toContain('sort=updated');
      expect(requests[3].url).toContain('label=env%3Dprod');
    });
  });

  describe('label 400 keeps previous data', () => {
    it('a non-OK label-commit response keeps the previously loaded agents', async () => {
      const projectId = 'p-label-400';
      // Name sort is complete-needing: the label commit drains, and the
      // drain's first (legacy) page is the one that fails.
      localStorage.setItem('scion-view-project-agents', 'grid');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'name', dir: 'asc' })
      );
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      let failNext = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        if (rawUrl.includes(`/api/v1/projects/${projectId}/agents?label=`) && failNext) {
          requests.push({ url: rawUrl });
          return Promise.resolve(jsonResponse({ error: { message: 'bad label' } }, 400));
        }
        return createFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(input, init);
      });

      const el = await createComponent(projectId);
      const before = (el as unknown as { agents: Agent[] }).agents;
      expect(before.length).toBe(5);

      failNext = true;
      const input = labelInput(el)!;
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);

      const after = (el as unknown as { agents: Agent[] }).agents;
      expect(after.length).toBe(5); // previous data kept, not cleared to []
      expect(requests.filter((r) => r.url.includes('label=')).length).toBe(1); // a 400 is not retried
      expect(internals(el).committedLabel).toBe('');
      expect(internals(el).agentWindow.state).toBe('small');
    });
  });

  describe('paged state: live updates, and reconnect', () => {
    /** 30 agents, forced paged (via `legacyTruncated` plus a `fit` below the count — see individual tests), 25/page: page 0 holds the 25 highest `updated`, page 1 holds the rest. */
    function mountForcedPaged(
      projectId: string,
      agents: Agent[],
      requests: AgentsRequest[]
    ): Promise<TestEl> {
      localStorage.setItem('scion-view-project-agents', 'list');
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          // `fit=0` forces the candidate count to always exceed it, so the
          // first response is paged regardless of how small the fixture is.
          u.searchParams.set('fit', '0');
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });
      return createComponent(projectId);
    }

    it('while paged, an SSE create and status for a project agent go through the window only: this.agents stays empty', async () => {
      const projectId = 'p-paged-gate';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect((el as unknown as { agents: Agent[] }).agents.length).toBe(0);
      expect(internals(el).agentStats.total).toBe(5);

      // A brand-new SSE-created agent for this project.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.created`,
        data: {
          agentId: 'a-gate',
          id: 'a-gate',
          name: 'a-gate',
          projectId,
          template: 't',
          phase: 'running',
          created: '2026-03-01T00:00:00Z',
          updated: '2026-03-01T00:00:00Z',
          messageMode: 'project',
        },
      });
      await flushLive(el);

      // A status delta for an already-known project agent too.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: agents[0].id, phase: 'stopped' },
      });
      await flushLive(el);

      // `mergeAgentsChanged` (the small/held-state merge) must never run
      // while paged — the window's own `applyChanges` is the only path.
      // If the gate were skipped, `this.agents` would have picked up the
      // new agent here instead of staying at its paged-state empty value.
      expect((el as unknown as { agents: Agent[] }).agents.length).toBe(0);
      // `agentStats` comes from the member index (paged state), not from
      // `this.agents`: 5 original members plus the new create.
      expect(internals(el).agentStats.total).toBe(6);
    });

    it('an off-page phase change with no phase filter is counts-only — stats update live, no chip, no request', async () => {
      const projectId = 'p-paged-counts';
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');

      await internals(el).agentWindow.next(); // page 1
      await el.updateComplete;
      const before = requests.length;
      expect(internals(el).agentStats.running).toBe(30);

      // agents[29] (highest `updated`, on page 0) goes from running to stopped.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: agents[29].id, phase: 'stopped' },
      });
      await flushLive(el);

      // A pure phase change off-page, with no active phase filter, does not
      // change which agent belongs on which page — it only affects the
      // live "Running" count, which shows no chip.
      expect(internals(el).agentStats.running).toBe(29);
      expect(internals(el).agentWindow.updatesAvailable).toBe(false);
      expect(requests.length).toBe(before);
    });

    it("a paged refetch's stats do not resurrect an agent already removed by an SSE delete", async () => {
      const projectId = 'p-paged-stats-tombstone';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentStats.total).toBe(5);

      // The hub tells this client one member is gone.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: agents[0].id },
      });
      await flushLive(el);
      expect(internals(el).agentStats.total).toBe(4);

      // A view-state change (sort direction flip) triggers a fresh
      // `loadAgentsForView` request, which always asks for `stats=1`. The
      // fixture's fetch handler still returns `stats.agents` built from the
      // full, static fixture list — it has no knowledge of the delete,
      // exactly like a REST response that was already in flight, or served
      // from a stale read replica, when the delete happened.
      internals(el).toggleSort('updated');
      await settle(el);
      await el.updateComplete;

      // The already-removed agent must not be re-counted.
      expect(internals(el).agentStats.total).toBe(4);
    });

    it('a paged refetch via a view-state change drops an agent already removed by an SSE delete', async () => {
      // Same race as the stats-only test above, through `loadAgentsForView`'s
      // own paged branch (the response's `agents` field, not just `stats`).
      const projectId = 'p-paged-fit-tombstone';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.items.some((a) => a.id === agents[0].id)).toBe(true);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: agents[0].id },
      });
      await flushLive(el);

      // A view-state change (sort direction flip) issues a fresh
      // `loadAgentsForView` request, landing in the paged branch again
      // (`fit` is forced to 0). The fixture still lists the already-deleted
      // agent.
      internals(el).toggleSort('updated');
      await settle(el);
      await el.updateComplete;

      expect(internals(el).agentWindow.items.some((a) => a.id === agents[0].id)).toBe(false);
    });

    it("the window's own page fetcher drops an agent already removed by an SSE delete", async () => {
      // Same race as the two tests above, but through the window's own
      // per-page fetcher (`fetchAgentsPage`, used by `next`/`prev`/
      // `refresh`) rather than `loadAgentsForView`'s own request.
      const projectId = 'p-paged-fetcher-tombstone';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.items.some((a) => a.id === agents[0].id)).toBe(true);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: agents[0].id },
      });
      await flushLive(el);

      // The chip-click refresh re-fetches page 0 through `fetchAgentsPage`;
      // the fixture still lists the already-deleted agent.
      await internals(el).agentWindow.refresh();
      await el.updateComplete;

      expect(internals(el).agentWindow.items.some((a) => a.id === agents[0].id)).toBe(false);
      // Page 0 also carries `stats` (`wantStats` is true at index 0), so
      // this also pins `fetchAgentsPage`'s own `freshStats` call, not just
      // its `agents` field.
      expect(internals(el).agentStats.total).toBe(4);
    });

    it("the window's own page fetcher tolerates a response with no agents field", async () => {
      // With a tombstone recorded, dropTombstoned iterates its input, so a
      // missing `agents` field must fall back to an empty list, not throw.
      const projectId = 'p-paged-fetcher-missing-agents';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: agents[0].id },
      });
      await flushLive(el);

      stubSortedAgentsWithoutList(projectId, globalThis.fetch);
      await internals(el).agentWindow.refresh();
      await el.updateComplete;

      expect(internals(el).agentWindow.error).toBeNull();
      expect(internals(el).agentWindow.items).toEqual([]);
    });

    it('an off-page change that newly passes the active phase filter raises the chip', async () => {
      const projectId = 'p-paged-newly-passes';
      const agents = Array.from({ length: 30 }, (_, i) =>
        makeAgent(i, { projectId, phase: i === 29 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      internals(el).setPhaseFilter('running');
      await settle(el); // the phase change's own one paged refetch
      await el.updateComplete;
      expect(internals(el).agentWindow.state).toBe('paged');

      await internals(el).agentWindow.next();
      await el.updateComplete;
      const before = requests.length;

      // agents[29] (off-page, currently filtered out) starts running.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: agents[29].id, phase: 'running' },
      });
      await flushLive(el);

      expect(internals(el).agentWindow.updatesAvailable).toBe(true);
      expect(requests.length).toBe(before); // still zero-cost
    });

    it('an off-page status change for a non-member under a different committed label never inflates stats.total and never raises the chip', async () => {
      const projectId = 'p-paged-label';
      const members = Array.from({ length: 30 }, (_, i) =>
        makeAgent(i, { projectId, labels: { env: 'prod' } })
      );
      const nonMember = makeAgent(999, { projectId, labels: { env: 'dev' }, phase: 'stopped' });
      const agents = [...members, nonMember];
      const requests: AgentsRequest[] = [];
      localStorage.setItem('scion-view-project-agents', 'list');
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0');
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      internals(el).committedLabel = 'env=prod'; // simulate having committed this label (bypasses UI for brevity)
      const input = labelInput(el)!;
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      await el.updateComplete;
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentStats.total).toBe(30); // only the 30 env=prod members

      const before = requests.length;
      // The non-member (env=dev, a different project-scope agent) sends a
      // status update. It is not on any page and was never a member, so the
      // off-page add rule (the add rule, mirroring the server's
      // stats population) must not add it.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: nonMember.id, phase: 'running' },
      });
      await flushLive(el);

      expect(internals(el).agentStats.total).toBe(30); // unchanged
      expect(internals(el).agentWindow.updatesAvailable).toBe(false); // ignored outright: no chip
      expect(requests.length).toBe(before);
    });

    it('on-page delete and phase-filter-failure still show the chip (backfill)', async () => {
      const projectId = 'p-paged-backfill';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.items.length).toBe(5);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: agents[0].id },
      });
      await flushLive(el);

      expect(internals(el).agentWindow.updatesAvailable).toBe(true);
      expect(internals(el).agentWindow.items.find((a) => a.id === agents[0].id)).toBeUndefined();
    });

    it('a reconnect of the live connection raises the chip with no request', async () => {
      const projectId = 'p-paged-resync';
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const el = await mountForcedPaged(projectId, agents, requests);
      expect(internals(el).agentWindow.state).toBe('paged');
      const before = requests.length;

      expect(internals(el).agentWindow.updatesAvailable).toBe(false);

      // A drop and reconnect of the live connection, through state.ts's own
      // resync detection.
      const sse = (stateManager as unknown as { sseClientInstance: EventTarget }).sseClientInstance;
      sse.dispatchEvent(new CustomEvent('disconnected'));
      sse.dispatchEvent(new CustomEvent('connected'));
      await settle(el);

      expect(internals(el).agentWindow.updatesAvailable).toBe(true);
      expect(requests.length).toBe(before);
    });

    describe('a deleted agent on the page, and a later create for it', () => {
      async function pagedWithDeleted(
        projectId: string,
        restore: boolean
      ): Promise<{ el: TestEl; requests: AgentsRequest[]; id: string }> {
        const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
        const requests: AgentsRequest[] = [];
        const el = await mountForcedPaged(projectId, agents, requests);
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        const id = win.items[0].id;
        const update = (subject: string, data: unknown): void =>
          (
            stateManager as unknown as {
              handleUpdate(u: { subject: string; data: unknown }): void;
            }
          ).handleUpdate({ subject, data });
        update(`project.${projectId}.agent.deleted`, { agentId: id });
        await flushLive(el);
        if (restore) {
          const restored = agents.find((a) => a.id === id)!;
          update(`project.${projectId}.agent.created`, { ...restored, agentId: id });
          await flushLive(el);
        }
        return { el, requests, id };
      }

      it('a restored agent the response still lists stays off the page and raises no chip on refresh, and repeated refreshes stay chip-free', async () => {
        const { el, requests, id } = await pagedWithDeleted('p-paged-restored', true);
        const win = internals(el).agentWindow;
        expect(stateManager.getAgent(id)).toBeUndefined();
        for (let i = 0; i < 3; i++) {
          const before = requests.length;
          await win.refresh();
          await settle(el);
          expect(requests.length - before).toBe(1);
          // The row is left out as deleted, but its delete predates the
          // request, so another refresh would leave it out the same way: no
          // backfill chip.
          expect(win.items).toHaveLength(24);
          expect(win.updatesAvailable).toBe(false);
          expect(win.items.some((a) => a.id === id)).toBe(false);
          expect(win.memberIndex.has(id)).toBe(false);
          expect(stateManager.getAgent(id)).toBeUndefined();
        }
      });

      it('a deleted agent the response still lists stays off the page and raises no chip on a later refresh', async () => {
        const { el, id } = await pagedWithDeleted('p-paged-deleted-listed', false);
        const win = internals(el).agentWindow;
        for (let i = 0; i < 2; i++) {
          await win.refresh();
          await settle(el);
          expect(win.items.some((a) => a.id === id)).toBe(false);
          expect(win.items).toHaveLength(24);
          expect(win.updatesAvailable).toBe(false);
        }
      });

      it('a delete during an in-flight refresh raises the chip for that refresh only', async () => {
        const projectId = 'p-paged-inflight-delete';
        const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
        const requests: AgentsRequest[] = [];
        const el = await mountForcedPaged(projectId, agents, requests);
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        const id = win.items[0].id;
        const h = holdable(
          globalThis.fetch as (
            input: string | URL | Request,
            init?: RequestInit
          ) => Promise<Response>,
          (u) => u.pathname === `/api/v1/projects/${projectId}/agents`
        );
        stubFetch(h.fn);

        h.hold();
        const refreshed = win.refresh();
        await vi.waitFor(() => expect(h.heldCount).toBe(1));
        (
          stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
        ).handleUpdate({ subject: `project.${projectId}.agent.deleted`, data: { agentId: id } });
        await flushLive(el);
        h.release();
        await refreshed;
        await settle(el);
        // The response predates the delete and still lists the row: the page
        // is one row short, and a refresh can fill it.
        expect(win.items.some((a) => a.id === id)).toBe(false);
        expect(win.items).toHaveLength(24);
        expect(win.updatesAvailable).toBe(true);

        // The next refresh's delete predates its request, so it raises no chip.
        await win.refresh();
        await settle(el);
        expect(h.sent).toHaveLength(2);
        expect(win.items.some((a) => a.id === id)).toBe(false);
        expect(win.items).toHaveLength(24);
        expect(win.updatesAvailable).toBe(false);
      });

      it('a delete applied during an in-flight refresh whose flush lands after the response raises the chip for that refresh only', async () => {
        const projectId = 'p-paged-inflight-unflushed-delete';
        const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
        const requests: AgentsRequest[] = [];
        const el = await mountForcedPaged(projectId, agents, requests);
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        const id = win.items[0].id;
        const h = holdable(
          globalThis.fetch as (
            input: string | URL | Request,
            init?: RequestInit
          ) => Promise<Response>,
          (u) => u.pathname === `/api/v1/projects/${projectId}/agents`
        );
        stubFetch(h.fn);

        h.hold();
        const refreshed = win.refresh();
        await vi.waitFor(() => expect(h.heldCount).toBe(1));
        const restoreFlush = deferLiveFlush();
        const watch = watchDeleteFlush(id, () => win.loading);
        try {
          (
            stateManager as unknown as {
              handleUpdate(u: { subject: string; data: unknown }): void;
            }
          ).handleUpdate({ subject: `project.${projectId}.agent.deleted`, data: { agentId: id } });
          expect(watch.loadingAtFlush).toBeUndefined();
          h.release();
          await refreshed;
          await settle(el);
        } finally {
          restoreFlush();
          watch.stop();
        }
        // The delete reached agents-changed only after the response was adopted.
        expect(watch.loadingAtFlush).toBe(false);
        // The response predates the delete and still lists the row: the page
        // is one row short, and a refresh can fill it.
        expect(win.items.some((a) => a.id === id)).toBe(false);
        expect(win.items).toHaveLength(24);
        expect(win.updatesAvailable).toBe(true);

        // The next refresh's delete predates its request, so it raises no chip.
        await win.refresh();
        await settle(el);
        expect(h.sent).toHaveLength(2);
        expect(win.items.some((a) => a.id === id)).toBe(false);
        expect(win.items).toHaveLength(24);
        expect(win.updatesAvailable).toBe(false);
      });
    });
  });

  describe('small state: live updates', () => {
    it('an SSE status delta updates the rendered list row, and an SSE create appears, with no re-adoption and no page reset', async () => {
      const projectId = 'p-small-live';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agentWindow.items.find((a) => a.id === 'a-4')?.phase).toBe('running');
      const beforeAgents = (el as unknown as { agents: Agent[] }).agents;
      const untouched = beforeAgents.find((a) => a.id === 'a-3');

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: 'a-4', phase: 'stopped' },
      });
      await flushLive(el);

      // The small-state list view must see the same live update grid/tree/
      // stats already saw via `this.agents` — no re-adoption step, and no
      // extra request.
      const afterAgents = (el as unknown as { agents: Agent[] }).agents;
      expect(afterAgents.find((a) => a.id === 'a-4')?.phase).toBe('stopped');
      expect(internals(el).agentWindow.items.find((a) => a.id === 'a-4')?.phase).toBe('stopped');
      expect(requests.length).toBe(1);
      // mergeChanged: the array identity changed (a-4 changed),
      // but every untouched agent is carried over by reference.
      expect(afterAgents).not.toBe(beforeAgents);
      expect(afterAgents.find((a) => a.id === 'a-3')).toBe(untouched);

      // A brand-new SSE-created agent, sorted to the top by `updated` desc, appears.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.created`,
        data: {
          agentId: 'a-new',
          id: 'a-new',
          name: 'brand-new',
          projectId,
          template: 't',
          phase: 'running',
          created: '2026-02-01T00:00:00Z',
          updated: '2026-02-01T00:00:00Z',
          messageMode: 'project',
        },
      });
      await flushLive(el);

      expect(internals(el).agentWindow.items.some((a) => a.id === 'a-new')).toBe(true);
      expect(requests.length).toBe(1); // still no request
    });

    it('a created event with a foreign projectId is not added (the project add rule)', async () => {
      const projectId = 'p-small-foreign-create';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: 3 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small');
      const before = (el as unknown as { agents: Agent[] }).agents.length;

      // Arrives on this project's own subject, but the payload names a
      // different project — the add rule gates on the agent object's own
      // `projectId`, not the subject it arrived on.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.created`,
        data: {
          agentId: 'a-foreign',
          id: 'a-foreign',
          name: 'a-foreign',
          projectId: 'some-other-project',
          template: 't',
          phase: 'running',
          created: '2026-04-01T00:00:00Z',
          updated: '2026-04-01T00:00:00Z',
          messageMode: 'project',
        },
      });
      await flushLive(el);

      const after = (el as unknown as { agents: Agent[] }).agents;
      expect(after.length).toBe(before);
      expect(after.some((a) => a.id === 'a-foreign')).toBe(false);
    });

    it('an SSE-created agent keeps its inherited capabilities across its next status delta', async () => {
      const projectId = 'p-small-caps';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: 2 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.created`,
        data: {
          agentId: 'a-caps',
          id: 'a-caps',
          name: 'a-caps',
          projectId,
          template: 't',
          phase: 'running',
          created: '2026-02-01T00:00:00Z',
          updated: '2026-02-01T00:00:00Z',
          messageMode: 'project',
          // No `_capabilities` of its own, as a real SSE create carries.
        },
      });
      await flushLive(el);

      const afterCreate = (el as unknown as { agents: Agent[] }).agents.find(
        (a) => a.id === 'a-caps'
      );
      expect(afterCreate?._capabilities).toBeTruthy();

      // Its next delta carries no `_capabilities` either, same as any real
      // status update.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: 'a-caps', phase: 'stopped' },
      });
      await flushLive(el);

      const afterStatus = (el as unknown as { agents: Agent[] }).agents.find(
        (a) => a.id === 'a-caps'
      );
      expect(afterStatus?.phase).toBe('stopped');
      expect(afterStatus?._capabilities).toBe(afterCreate?._capabilities);
    });

    it('a REST response landing after an SSE delete does not resurrect the deleted agent', async () => {
      const projectId = 'p-small-tombstone';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: 3 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agentWindow.items.some((a) => a.id === 'a-1')).toBe(true);

      // The hub tells this client 'a-1' is gone.
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: 'a-1' },
      });
      await flushLive(el);
      expect(internals(el).agentWindow.items.some((a) => a.id === 'a-1')).toBe(false);

      // A lifecycle refresh re-fetches, and the fixture's fetch handler still
      // returns the original fixture list — 'a-1' included — because it has
      // no knowledge of the delete (exactly like a REST response that was
      // already in flight, or served from a stale read replica, when the
      // delete happened). The already-tombstoned ID must not reappear.
      internals(el).backgroundRefresh('lifecycle-refresh');
      await flushLive(el);

      expect((el as unknown as { agents: Agent[] }).agents.some((a) => a.id === 'a-1')).toBe(false);
      expect(internals(el).agentWindow.items.some((a) => a.id === 'a-1')).toBe(false);
    });

    it('a legacy-mode reload drops an agent already removed by an SSE delete', async () => {
      // Same race, through `loadLegacyAgentsImpl` instead of the sorted-mode
      // load above: grid view (the default) is not sorted-mode eligible, so
      // every load here goes through the legacy path.
      const projectId = 'p-legacy-tombstone';
      const agents = Array.from({ length: 3 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect((el as unknown as { agents: Agent[] }).agents.some((a) => a.id === 'a-1')).toBe(true);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: 'a-1' },
      });
      await flushLive(el);

      internals(el).backgroundRefresh('lifecycle-refresh');
      await flushLive(el);

      expect((el as unknown as { agents: Agent[] }).agents.some((a) => a.id === 'a-1')).toBe(false);
    });

    it('a page-level load tolerates a response with no agents field', async () => {
      // With a tombstone recorded, dropTombstoned iterates its input, so a
      // missing `agents` field must fall back to an empty list, not throw.
      const projectId = 'p-small-missing-agents';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: 3 }, (_, i) => makeAgent(i));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createFetchHandler({ projectId, projectCaps: { actions: ['read'] }, agents, requests })
      );

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small');

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.deleted`,
        data: { agentId: 'a-1' },
      });
      await flushLive(el);

      stubSortedAgentsWithoutList(projectId, globalThis.fetch);
      const warn = vi.spyOn(console, 'warn');
      internals(el).backgroundRefresh('lifecycle-refresh');
      await vi.waitFor(() => {
        expect((el as unknown as { agents: Agent[] }).agents).toEqual([]);
      });
      await el.updateComplete;

      expect(warn).not.toHaveBeenCalledWith('Background refresh failed:', expect.anything());
      expect(internals(el).agentWindow.items).toEqual([]);
    });
  });

  describe('view changes that need the complete set drain it once, then cost nothing', () => {
    function stubAlwaysPaged(projectId: string, agents: Agent[], requests: AgentsRequest[]) {
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // every sorted request in this test stays paged
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });
    }

    it('sort -> name while paged drains once into the local set; later sort, dir and phase changes cost zero', async () => {
      const projectId = 'p-name-drain';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubAlwaysPaged(projectId, agents, requests);

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');

      let n = requests.length;
      internals(el).toggleSort('name');
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).not.toContain('sort=');
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agents.length).toBe(30);

      n = requests.length;
      internals(el).toggleSort('updated');
      internals(el).toggleSort('updated'); // dir flip
      internals(el).toggleSort('created');
      internals(el).setPhaseFilter('running');
      await settle(el);
      expect(requests.length - n).toBe(0);
      expect(internals(el).agentWindow.state).toBe('small');
    });

    it('always paged: a dir flip and a created sort cost one request each, and list <-> grid toggles cost zero', async () => {
      const projectId = 'p-always-paged';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubAlwaysPaged(projectId, agents, requests);

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');

      let n = requests.length;
      internals(el).toggleSort('updated'); // same field: flips dir only
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain('sort=updated');
      expect(requests[requests.length - 1].url).toContain('dir=asc');

      n = requests.length;
      internals(el).toggleSort('created');
      await settle(el);
      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain('sort=created');
      expect(internals(el).agentWindow.state).toBe('paged');

      const toggle = viewToggle(el)!;
      const perToggleCosts: number[] = [];
      for (const v of ['grid', 'list', 'grid', 'list', 'grid', 'list']) {
        n = requests.length;
        toggle.dispatchEvent(new CustomEvent('view-change', { detail: { view: v } }));
        await settle(el);
        perToggleCosts.push(requests.length - n);
      }
      expect(perToggleCosts).toEqual([0, 0, 0, 0, 0, 0]);
      expect(internals(el).agentWindow.state).toBe('paged');
    });
  });

  describe('costs of the earlier sorted-list cut are gone', () => {
    const mountPaged = async (projectId: string, view: 'list' | 'grid') => {
      localStorage.setItem('scion-view-project-agents', view);
      const agents = Array.from({ length: 100 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const posts: Array<{ resolve: (v: Response) => void }> = [];
      const inner = createRealisticFetchHandler({
        projectId,
        projectCaps: { actions: ['read'] },
        agents,
        requests,
      });
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        if ((init?.method ?? 'GET').toUpperCase() === 'POST') {
          // Lifecycle actions hang, so only the optimistic update can change a row.
          const d = deferred<Response>();
          posts.push(d);
          return d.promise;
        }
        return inner(input, init);
      });
      const el = await createComponent(projectId);
      const agentGets = () =>
        sentUrls.filter((url) => url.includes(`/api/v1/projects/${projectId}/agents`)).length;
      return { el, requests, agentGets, posts };
    };

    it('paged -> grid issues no request and renders the server page; paged -> tree and paged -> name sort each start one drain', async () => {
      const a = await mountPaged('p-gone-grid', 'list');
      expect(internals(a.el).agentWindow.state).toBe('paged');
      let n = a.agentGets();
      viewToggle(a.el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'grid' } }));
      await settle(a.el);
      await a.el.updateComplete;
      expect(a.agentGets() - n).toBe(0);
      expect(internals(a.el).agentWindow.state).toBe('paged');
      expect(a.el.shadowRoot!.querySelectorAll('.agent-grid > *').length).toBe(25);

      n = a.agentGets();
      viewToggle(a.el)!.dispatchEvent(
        new CustomEvent('view-change', { detail: { view: 'graph' } })
      );
      await settle(a.el);
      expect(a.agentGets() - n).toBe(1);
      expect(a.requests[a.requests.length - 1].url).toContain('limit=500');
      expect(a.requests[a.requests.length - 1].url).not.toContain('sort=');
      expect(internals(a.el).agentWindow.state).toBe('small');
      a.el.remove();
      localStorage.clear();

      const b = await mountPaged('p-gone-name', 'grid');
      expect(internals(b.el).agentWindow.state).toBe('paged');
      n = b.agentGets();
      internals(b.el).toggleSort('name');
      await settle(b.el);
      expect(b.agentGets() - n).toBe(1);
      expect(b.requests[b.requests.length - 1].url).not.toContain('sort=');
      expect(internals(b.el).agentWindow.state).toBe('small');
    });

    it('a grid page load followed by a switch to list issues no extra request', async () => {
      const { el, requests, agentGets } = await mountPaged('p-gone-fit', 'grid');
      expect(agentGets()).toBe(1);
      expect(requests[0].url).toContain('sort=updated');
      expect(internals(el).agentWindow.state).toBe('paged');

      viewToggle(el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'list' } }));
      await settle(el);
      expect(agentGets()).toBe(1);
      expect(internals(el).agentWindow.state).toBe('paged');
    });

    it('an optimistic lifecycle update applies to the paged row before the server answers, with no agents request', async () => {
      const { el, agentGets, posts } = await mountPaged('p-gone-optimistic', 'list');
      expect(internals(el).agentWindow.state).toBe('paged');
      const target = internals(el).agentWindow.items[3];
      expect(target.phase).toBe('running');

      const n = agentGets();
      void internals(el).handleAgentAction(target.id, 'stop');
      await settle(el);
      expect(posts.length).toBe(1); // still in flight
      expect(agentGets() - n).toBe(0);
      const row = internals(el).agentWindow.items.find((a) => a.id === target.id);
      expect(row?.phase).toBe('stopping');
      expect(internals(el).agentWindow.items[3].id).toBe(target.id); // stays in place
      expect(internals(el).agents).toEqual([]); // the page-level set stays empty while paged
    });
  });

  describe('request counts per interaction, one test per agent count', () => {
    type Check = (el: TestEl, agents: Agent[]) => void;
    type Step = [label: string, run: (el: TestEl) => void | Promise<void>, check?: Check];
    const commitLabel = (el: TestEl, value: string) => {
      const input = labelInput(el)!;
      input.value = value;
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
    };
    const clearLabel = (el: TestEl) => {
      const input = labelInput(el)!;
      input.value = '';
      input.dispatchEvent(new Event('sl-clear'));
    };
    const setView = (el: TestEl, view: string) =>
      viewToggle(el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view } }));
    const stopAll = async (el: TestEl) => {
      await internals(el).handleStopAll();
    };
    const reconnect = (el: TestEl) => {
      void el;
      const sse = (stateManager as unknown as { sseClientInstance: EventTarget }).sseClientInstance;
      sse.dispatchEvent(new CustomEvent('disconnected'));
      sse.dispatchEvent(new CustomEvent('connected'));
    };
    /**
     * The rendered page holds only agents passing `keep`, and as many as
     * fit on the page: a filter that was ignored would leave other agents
     * on it, and one applied twice would leave too few.
     */
    const rowsMatch =
      (keep: (a: Agent) => boolean): Check =>
      (el, agents) => {
        const w = internals(el).agentWindow;
        const items = w.items;
        // A capped set holds only what the drain loaded.
        const pool = w.state === 'capped' ? internals(el).agents : agents;
        const expected = pool.filter(keep).length;
        expect(items.every(keep)).toBe(true);
        expect(items.length).toBe(Math.min(internals(el).pagerPageSize, expected));
        const rows = el.shadowRoot!.querySelectorAll('.agent-table-container tbody tr');
        expect(rows.length).toBe(items.length);
      };
    const onPage =
      (index: number): Check =>
      (el) =>
        expect(internals(el).agentWindow.pageIndex).toBe(index);
    const signalsReconnect: Check = (el) => {
      const w = internals(el).agentWindow;
      if (w.state === 'paged') {
        expect(w.updatesAvailable).toBe(true);
      } else if (w.state === 'capped') {
        // The capped banner wins over the stale one; the stale flag is still set.
        expect(w.stale).toBe(true);
        expect(el.shadowRoot!.querySelector('.agent-window-banner')?.textContent).toContain(
          'more exist'
        );
      } else {
        expect(el.shadowRoot!.querySelector('.agent-window-banner')?.textContent).toContain(
          'may be stale'
        );
      }
    };
    // Each step is one interaction of the request-count table: a view
    // switch, a sort or dir change, a phase change, page navigation, a page
    // size change, label typing, a label commit or clear (sl-change or
    // sl-clear), a lifecycle or stop-all refresh, a reconnect, and the chip.
    const steps: Step[] = [
      ['switch to grid view', (el) => setView(el, 'grid')],
      ['switch to list view', (el) => setView(el, 'list'), rowsMatch(() => true)],
      ['flip the updated sort direction', (el) => internals(el).toggleSort('updated')],
      ['sort by created', (el) => internals(el).toggleSort('created')],
      ['sort by updated after created', (el) => internals(el).toggleSort('updated')],
      [
        'filter phase running',
        (el) => internals(el).setPhaseFilter('running'),
        rowsMatch((a) => a.phase === 'running'),
      ],
      ['clear the phase filter', (el) => internals(el).setPhaseFilter(''), rowsMatch(() => true)],
      [
        'next page',
        (el) => internals(el).agentWindow.next(),
        (el, agents) => onPage(agents.length > 25 ? 1 : 0)(el, agents),
      ],
      ['previous page', (el) => internals(el).agentWindow.prev(), onPage(0)],
      ['page size 50', (el) => internals(el).onPagerSizeChange(50), rowsMatch(() => true)],
      ['page size back to 25', (el) => internals(el).onPagerSizeChange(25), rowsMatch(() => true)],
      [
        'type a label without committing it',
        (el) => {
          const input = labelInput(el)!;
          input.value = 'env=pr';
          input.dispatchEvent(new Event('sl-input'));
        },
      ],
      [
        'commit label env=prod',
        (el) => commitLabel(el, 'env=prod'),
        rowsMatch((a) => a.labels?.env === 'prod'),
      ],
      ['clear label env=prod with sl-clear', (el) => clearLabel(el), rowsMatch(() => true)],
      [
        'commit bare-key label team',
        (el) => commitLabel(el, 'team'),
        rowsMatch((a) => a.labels?.team !== undefined),
      ],
      ['clear bare-key label team with sl-clear', (el) => clearLabel(el), rowsMatch(() => true)],
      ['lifecycle refresh', (el) => internals(el).backgroundRefresh('lifecycle-refresh')],
      ['stop-all refresh', stopAll],
      ['live connection reconnect', reconnect, signalsReconnect],
      ['switch to tree view', (el) => setView(el, 'graph')],
      ['switch from tree back to list view', (el) => setView(el, 'list')],
      ['sort by name', (el) => internals(el).toggleSort('name')],
      ['sort by updated after the name sort', (el) => internals(el).toggleSort('updated')],
      [
        'flip the updated sort direction after the tree',
        (el) => internals(el).toggleSort('updated'),
      ],
      [
        'filter phase running after the tree',
        (el) => internals(el).setPhaseFilter('running'),
        rowsMatch((a) => a.phase === 'running'),
      ],
      ['next page after the tree', (el) => internals(el).agentWindow.next()],
      [
        'lifecycle refresh after the tree',
        (el) => internals(el).backgroundRefresh('lifecycle-refresh'),
      ],
      ['stop-all refresh after the tree', stopAll],
      ['counts chip click', (el) => internals(el).backgroundRefresh('chip')],
    ];

    /** Every third agent stopped; even agents env=prod, odd env=dev; every fifth also has a bare `team` key. */
    const mixedAgents = (count: number, projectId: string): Agent[] =>
      Array.from({ length: count }, (_, i) =>
        makeAgent(i, {
          projectId,
          phase: i % 3 === 0 ? 'stopped' : 'running',
          labels: {
            env: i % 2 === 0 ? 'prod' : 'dev',
            ...(i % 5 === 0 ? { team: 'core' } : {}),
          },
        })
      );

    const cases: Array<{
      name: string;
      count: number;
      costs: number[];
      states: string[];
      load?: { requests: number; state: string; banner: string };
    }> = [
      {
        name: '25 agents (complete, so small): only commits and refreshes send a request',
        count: 25,
        // prettier-ignore
        costs: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1],
        // prettier-ignore
        states: [
          'small', 'small', 'small', 'small', 'small', 'small', 'small', 'small',
          'small', 'small', 'small', 'small', 'small', 'small', 'small', 'small',
          'small', 'small', 'small', 'small', 'small', 'small', 'small', 'small',
          'small', 'small', 'small', 'small', 'small',
        ],
      },
      {
        name: '60 agents at page size 25 (above the 50-agent fit threshold, so paged): next and prev are real page requests',
        count: 60,
        // prettier-ignore
        costs: [0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 0, 1, 0, 0, 0, 0, 0, 0, 1, 1, 1],
        // prettier-ignore
        states: [
          'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged',
          'paged', 'paged', 'paged', 'paged', 'small', 'paged', 'small', 'paged',
          'paged', 'paged', 'paged', 'small', 'small', 'small', 'small', 'small',
          'small', 'small', 'paged', 'paged', 'paged',
        ],
      },
      {
        name: '500 agents (above the 50-agent fit threshold, so paged): each server-side change costs one request',
        count: 500,
        // prettier-ignore
        costs: [0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 1, 1, 1, 1, 0, 1, 0, 0, 0, 0, 0, 0, 1, 1, 1],
        // prettier-ignore
        states: [
          'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged',
          'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'small', 'paged',
          'paged', 'paged', 'paged', 'small', 'small', 'small', 'small', 'small',
          'small', 'small', 'paged', 'paged', 'paged',
        ],
      },
      {
        name: '1,200 agents (paged; the tree drains three pages into a held set, after which changes are free)',
        count: 1200,
        // prettier-ignore
        costs: [0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1, 0, 1, 1, 3, 1, 1, 1, 0, 3, 0, 0, 0, 0, 0, 0, 0, 0, 3],
        // prettier-ignore
        states: [
          'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged',
          'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'held', 'paged',
          'paged', 'paged', 'paged', 'held', 'held', 'held', 'held', 'held',
          'held', 'held', 'held', 'held', 'held',
        ],
      },
      {
        name: '2,001 agents (above the 2,000 candidate ceiling, so sorted requests are refused and the set is a capped drain; a k=v label that narrows the set pages again)',
        count: 2001,
        // prettier-ignore
        costs: [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 4, 4, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 4],
        // prettier-ignore
        states: [
          'capped', 'capped', 'capped', 'capped', 'capped', 'capped', 'capped', 'capped',
          'capped', 'capped', 'capped', 'capped', 'paged', 'capped', 'capped', 'capped',
          'capped', 'capped', 'capped', 'capped', 'capped', 'capped', 'capped', 'capped',
          'capped', 'capped', 'capped', 'capped', 'capped',
        ],
        load: {
          requests: 5, // the refused sorted request, then four drain pages
          state: 'capped',
          banner: '2,000 loaded (newest 2,000 checked), more exist',
        },
      },
    ];

    for (const c of cases) {
      it(
        c.name,
        async () => {
          const projectId = `p-counts-${c.count}`;
          localStorage.setItem('scion-view-project-agents', 'list');
          const agents = mixedAgents(c.count, projectId);
          const requests: AgentsRequest[] = [];
          stubFetch(
            createRealisticFetchHandler({
              projectId,
              projectCaps: { actions: ['read', 'stop_all'] },
              agents,
              requests,
              refuseSortedAbove: 2000,
            })
          );
          const el = await createComponent(projectId);
          await settle(el);
          expect(requests[0].url).toContain('sort=updated');
          if (c.load) {
            expect(requests.length).toBe(c.load.requests);
            expect(internals(el).agentWindow.state).toBe(c.load.state);
            expect(el.shadowRoot!.querySelector('.agent-window-banner')?.textContent).toContain(
              c.load.banner
            );
          } else {
            expect(requests.length).toBe(1);
            expect(internals(el).agentWindow.state).toBe(
              c.count > PROJECT_AGENTS_FIT_THRESHOLD ? 'paged' : 'small'
            );
          }

          const costs: number[] = [];
          const states: string[] = [];
          for (const [name, run, check] of steps) {
            const n = requests.length;
            await run(el);
            await settle(el);
            costs.push(requests.length - n);
            states.push(internals(el).agentWindow.state);
            if (check) {
              try {
                check(el, agents);
              } catch (err) {
                throw new Error(`step "${name}": ${(err as Error).message}`, { cause: err });
              }
            }
          }
          const named = (xs: Array<number | string>) =>
            steps.map(([name], i) => `${name}: ${xs[i]}`);
          expect(named(costs)).toEqual(named(c.costs));
          expect(named(states)).toEqual(named(c.states));
        },
        60_000
      );
    }
  });
}, 20_000);

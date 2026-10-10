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
 * Drain caps, in-flight live changes, paging, and failed view changes.
 *
 * One part of the "project-detail — agent list window" suite. The suite is split
 * across project-detail-agent-window.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/project-detail-agent-window.ts.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Agent } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import { holdable } from './__fixtures__/global-agents-endpoint.js';
import {
  jsonResponse,
  makeAgent,
  type AgentsRequest,
  type TestEl,
  stubFetch,
  createComponent,
  labelInput,
  viewToggle,
  internals,
  pager,
  flushLive,
  settle,
  deferred,
  createRealisticFetchHandler,
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

  describe('a drain that hits its request cap', () => {
    it('is capped: the tree and list show the banner, the list pager shows the capped total, lifecycle refresh is free and Refresh drains again', async () => {
      const projectId = 'p-capped';
      localStorage.setItem('scion-view-project-agents', 'graph');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'name', dir: 'asc' })
      );
      const agents = Array.from({ length: 100 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
          legacyTruncated: true, // 20 per page: four pages leave more behind
        })
      );
      const el = await createComponent(projectId);
      await vi.waitFor(() => expect(internals(el).agentWindow.state).toBe('capped'));
      await el.updateComplete;
      expect(requests.length).toBe(4);
      expect(requests.every((r) => !r.url.includes('sort='))).toBe(true);
      expect(internals(el).agents.length).toBe(80);
      const banner = el.shadowRoot!.querySelector('.agent-window-banner');
      expect(banner?.textContent).toContain('80 loaded (newest 2,000 checked), more exist');

      let n = requests.length;
      internals(el).backgroundRefresh('lifecycle-refresh');
      await settle(el);
      expect(requests.length - n).toBe(0);

      // Name sort is not sorted-eligible, so the list view stays capped.
      viewToggle(el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'list' } }));
      await settle(el);
      await el.updateComplete;
      expect(requests.length - n).toBe(0);
      expect(internals(el).agentWindow.state).toBe('capped');
      expect(el.shadowRoot!.querySelector('.agent-window-banner')?.textContent).toContain(
        '80 loaded (newest 2,000 checked), more exist'
      );
      const pg = pager(el)!;
      await (pg as unknown as { updateComplete: Promise<boolean> }).updateComplete;
      expect(pg.shadowRoot?.textContent).toContain('80 loaded (newest 2,000 checked), more exist');

      viewToggle(el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view: 'graph' } }));
      await el.updateComplete;
      n = requests.length;
      const refresh = el.shadowRoot!.querySelector('.agent-window-banner sl-tag') as HTMLElement;
      refresh.click();
      await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false));
      await settle(el);
      expect(requests.length - n).toBe(4);
      expect(internals(el).agentWindow.state).toBe('capped');
    });
  });

  describe('a capped set shows the banner in every view rendered from it', () => {
    async function mountCapped(
      projectId: string,
      view: string,
      sortField: string
    ): Promise<{ el: TestEl; requests: AgentsRequest[] }> {
      localStorage.setItem('scion-view-project-agents', view);
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: sortField, dir: 'asc' })
      );
      const agents = Array.from({ length: 100 }, (_, i) =>
        makeAgent(i, { projectId, labels: { env: 'prod' } })
      );
      const requests: AgentsRequest[] = [];
      stubFetch(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
          legacyTruncated: true,
        })
      );
      const el = await createComponent(projectId);
      return { el, requests };
    }

    const bannerText = (el: TestEl) =>
      el.shadowRoot!.querySelector('.agent-window-banner')?.textContent ?? '';

    for (const [view, sortField] of [
      ['grid', 'name'],
      ['list', 'name'],
      ['list', 'status'],
      ['graph', 'updated'],
    ] as const) {
      it(`${view} view with ${sortField} sort`, async () => {
        const { el } = await mountCapped(`p-capped-${view}-${sortField}`, view, sortField);
        await vi.waitFor(() => expect(internals(el).agentWindow.state).toBe('capped'));
        await el.updateComplete;
        expect(bannerText(el)).toContain('80 loaded (newest 2,000 checked), more exist');
      });
    }

    it('a bare-key label in the list view with updated sort', async () => {
      const { el, requests } = await mountCapped('p-capped-bare-key', 'list', 'updated');
      // Sorted-eligible: the first request is the fit request, and it pages.
      expect(requests[0].url).toContain('sort=updated');
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(bannerText(el)).toBe('');

      const input = labelInput(el)!;
      input.value = 'env';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await vi.waitFor(() => expect(internals(el).agentWindow.state).toBe('capped'));
      await el.updateComplete;
      expect(bannerText(el)).toContain('80 loaded (newest 2,000 checked), more exist');
    });
  });

  describe('a live create while a seeding request is in flight is never lost', () => {
    const emitCreate = (projectId: string, id: string) =>
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.created`,
        data: {
          agentId: id,
          id,
          name: id,
          projectId,
          template: 't',
          phase: 'running',
          created: '2026-03-01T00:00:00Z',
          updated: '2026-03-01T00:00:00Z',
          messageMode: 'project',
        },
      });

    /**
     * Mounts the page with agents GETs answered by the realistic handler.
     * The first agents GET (and the next one after each `hold()`) computes
     * its response at once, so the server snapshot predates any create the
     * test emits, and returns it only when the test calls `release()`.
     */
    async function mountHeld(projectId: string, view: string, sortField: string, count: number) {
      localStorage.setItem('scion-view-project-agents', view);
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: sortField, dir: 'desc' })
      );
      const agents = Array.from({ length: count }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const inner = createRealisticFetchHandler({
        projectId,
        projectCaps: { actions: ['read'] },
        agents,
        requests,
      });
      let armed = true;
      let held: { url: string; gate: ReturnType<typeof deferred<void>> } | null = null;
      stubFetch(async (input: string | URL | Request, init?: RequestInit) => {
        const url =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const res = await inner(input, init);
        const isAgentsGet =
          new URL(url, 'http://localhost').pathname === `/api/v1/projects/${projectId}/agents`;
        if (armed && isAgentsGet) {
          armed = false;
          const gate = deferred<void>();
          held = { url, gate };
          await gate.promise;
        }
        return res;
      });
      const el = await createComponent(projectId, { holdsFirstLoad: true });
      const waitHeld = async (): Promise<string> => {
        await vi.waitFor(() => expect(held).not.toBeNull());
        return held!.url;
      };
      const release = async (): Promise<void> => {
        const h = held!;
        held = null;
        h.gate.resolve();
        await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false));
        await settle(el);
        await el.updateComplete;
      };
      const hold = (): void => {
        armed = true;
      };
      return { el, requests, waitHeld, release, hold };
    }

    const flushSse = async (el: TestEl) => {
      await flushLive(el);
    };

    it('the fit request answering complete: the create joins the small set', async () => {
      const projectId = 'p-live-fit-small';
      const m = await mountHeld(projectId, 'list', 'updated', 10);
      expect(await m.waitHeld()).toContain('fit=');

      emitCreate(projectId, 'a-live');
      await flushSse(m.el);
      await m.release();

      expect(internals(m.el).agentWindow.state).toBe('small');
      expect(internals(m.el).agents.map((a) => a.id)).toContain('a-live');
      expect(internals(m.el).agentWindow.items[0].id).toBe('a-live');
      expect(internals(m.el).agentStats.total).toBe(11);
    });

    it('the fit request answering paged: the create joins the member index and raises the chip', async () => {
      const projectId = 'p-live-fit-paged';
      const m = await mountHeld(projectId, 'list', 'updated', 60);
      expect(await m.waitHeld()).toContain('fit=');

      emitCreate(projectId, 'a-live');
      await flushSse(m.el);
      await m.release();

      expect(internals(m.el).agentWindow.state).toBe('paged');
      expect(internals(m.el).agentStats.total).toBe(61);
      expect(internals(m.el).agentWindow.updatesAvailable).toBe(true);
      expect(m.requests.length).toBe(1);
    });

    it('above the fit threshold: an off-page phase change while the paged fit request is in flight reaches the running count', async () => {
      const projectId = 'p-live-fit-unknown';
      const m = await mountHeld(projectId, 'list', 'updated', 60);
      expect(await m.waitHeld()).toContain('fit=');
      // The oldest agent sorts last, off page 0, and is not in the store yet.
      expect(stateManager.getAgent('a-0')).toBeUndefined();

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: 'a-0', phase: 'stopped' },
      });
      await flushSse(m.el);
      await m.release();

      expect(internals(m.el).agentWindow.state).toBe('paged');
      expect(internals(m.el).agentWindow.items.some((a) => a.id === 'a-0')).toBe(false);
      expect(internals(m.el).agentStats).toMatchObject({ total: 60, running: 59 });
      expect(m.requests.length).toBe(1);
    });

    it('the legacy first request: the create joins the drained set', async () => {
      const projectId = 'p-live-legacy';
      const m = await mountHeld(projectId, 'list', 'name', 10);
      expect(await m.waitHeld()).not.toContain('sort=');

      emitCreate(projectId, 'a-live');
      await flushSse(m.el);
      await m.release();

      expect(internals(m.el).agentWindow.state).toBe('small');
      expect(internals(m.el).agents.map((a) => a.id)).toContain('a-live');
      expect(internals(m.el).agentStats.total).toBe(11);
    });

    it('a paged page request with stats: the create survives the member index re-seed', async () => {
      const projectId = 'p-live-page';
      const m = await mountHeld(projectId, 'list', 'updated', 60);
      await m.waitHeld();
      await m.release();
      expect(internals(m.el).agentWindow.state).toBe('paged');
      expect(internals(m.el).agentStats.total).toBe(60);

      // The paged chip refetches page 0 with stats=1.
      m.hold();
      const refreshed = internals(m.el).agentWindow.refresh();
      expect(await m.waitHeld()).toContain('stats=1');
      emitCreate(projectId, 'a-live');
      await flushSse(m.el);
      await m.release();
      await refreshed;
      await m.el.updateComplete;

      expect(internals(m.el).agentStats.total).toBe(61);
      expect(internals(m.el).agentWindow.updatesAvailable).toBe(true);
    });

    it('a live status change during a paged page request is reflected in the adopted page', async () => {
      const projectId = 'p-live-page-update';
      const m = await mountHeld(projectId, 'list', 'updated', 60);
      await m.waitHeld();
      await m.release();
      const last = internals(m.el).agentWindow.items.at(-1)!;

      m.hold();
      const refreshed = internals(m.el).agentWindow.refresh();
      await m.waitHeld();
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: last.id, phase: 'stopped' },
      });
      await flushSse(m.el);
      await m.release();
      await refreshed;

      const row = internals(m.el).agentWindow.items.find((a) => a.id === last.id);
      expect(row?.phase).toBe('stopped');
    });
  });

  describe('every adopted response keeps the live changes that land while it is in flight', () => {
    /** The window surface these tests read. */
    interface MatrixWindow {
      state: string;
      items: Agent[];
      updatesAvailable: boolean;
      stale: boolean;
      banner: { kind: string } | null;
      stats: { total: number; running: number };
      memberIndex: { has(id: string): boolean; getPhase(id: string): string | undefined };
      next(): Promise<void>;
      refresh(): Promise<void>;
    }

    /**
     * One way the page adopts an agents response. `start` mounts the page
     * and returns once that response's request is held; the test then
     * applies one live change, releases the request and checks the result.
     */
    interface AdoptRow {
      name: string;
      count: number;
      /** The persisted sort field: `name` needs the complete set, so the page drains. */
      sortField: 'updated' | 'name';
      /** Hold the page's own first request, or a window page fetch after it settled. */
      fetchPage?: (win: MatrixWindow) => Promise<void>;
      /** The adopted result is the complete set (small), not a server page. */
      local: boolean;
      /** An agent the adopted response lists (on the adopted page, or in the set). */
      row: string;
      /** Paged: a member that is not on the adopted page. */
      offPage: string;
      /** Paged: an activity time that sorts onto the adopted page. */
      onPageKey: string;
      /** Paged: a brand-new agent could land on the adopted page. */
      createLands: boolean;
    }

    // 60 agents page as a-59..a-35 (page 0), a-34..a-10 (page 1), a-9..a-0.
    const page0Key = '2026-03-01T00:00:00Z';
    const rows: AdoptRow[] = [
      {
        name: 'the complete fit response',
        count: 10,
        sortField: 'updated',
        local: true,
        row: 'a-7',
        offPage: '',
        onPageKey: '',
        createLands: false,
      },
      {
        name: 'the paged fit response',
        count: 60,
        sortField: 'updated',
        local: false,
        row: 'a-50',
        offPage: 'a-0',
        onPageKey: page0Key,
        createLands: true,
      },
      {
        name: 'a Next response',
        count: 60,
        sortField: 'updated',
        fetchPage: (win) => win.next(),
        local: false,
        row: 'a-30',
        offPage: 'a-0',
        onPageKey: '2026-01-02T00:00:20.500Z',
        createLands: false,
      },
      {
        name: 'a chip refresh response',
        count: 60,
        sortField: 'updated',
        fetchPage: (win) => win.refresh(),
        local: false,
        row: 'a-50',
        offPage: 'a-0',
        onPageKey: page0Key,
        createLands: true,
      },
      {
        name: 'a drain response',
        count: 60,
        sortField: 'name',
        local: true,
        row: 'a-8',
        offPage: '',
        onPageKey: '',
        createLands: false,
      },
    ];

    let seq = 0;
    const update = (subject: string, data: unknown) =>
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({ subject, data });
    const reconnect = () => {
      const sse = (stateManager as unknown as { sseClientInstance: EventTarget }).sseClientInstance;
      sse.dispatchEvent(new CustomEvent('disconnected'));
      sse.dispatchEvent(new CustomEvent('connected'));
    };

    describe.each(rows)('$name', (row) => {
      let el: TestEl;
      let h: ReturnType<typeof holdable>;
      let projectId: string;
      const win = (): MatrixWindow => internals(el).agentWindow as unknown as MatrixWindow;
      const shown = (id: string): Agent | undefined =>
        row.local
          ? (internals(el) as unknown as { agents: Agent[] }).agents.find((a) => a.id === id)
          : win().items.find((a) => a.id === id);
      const agentOf = (id: string): Agent => makeAgent(Number(id.slice(2)), { projectId });

      beforeEach(async () => {
        projectId = `p-adopt-${++seq}`;
        localStorage.setItem('scion-view-project-agents', 'list');
        localStorage.setItem(
          `scion-sort-project-agents-${projectId}`,
          JSON.stringify({ field: row.sortField, dir: 'desc' })
        );
        const agents = Array.from({ length: row.count }, (_, i) => makeAgent(i, { projectId }));
        h = holdable(
          createRealisticFetchHandler({
            projectId,
            projectCaps: { actions: ['read'] },
            agents,
            requests: [],
          }),
          (u) => u.pathname === `/api/v1/projects/${projectId}/agents`
        );
        stubFetch(h.fn);
        if (row.fetchPage) {
          el = await createComponent(projectId);
          expect(win().state).toBe('paged');
          h.hold();
          void row.fetchPage(win());
        } else {
          h.hold();
          el = await createComponent(projectId, { holdsFirstLoad: true });
        }
        await vi.waitFor(() => expect(h.heldCount).toBe(1));
      });

      // The hooks of every row live until the file ends; drop this row's
      // page and fake so they do not stay on the heap after its tests.
      afterEach(() => {
        el = undefined as unknown as TestEl;
        h = undefined as unknown as ReturnType<typeof holdable>;
      });

      const done = async (): Promise<void> => {
        await flushLive(el);
        h.release();
        await settle(el);
        expect(win().state).toBe(row.local ? 'small' : 'paged');
      };

      it('with no live change: no chip and no stale banner', async () => {
        await done();
        expect(win().updatesAvailable).toBe(false);
        expect(win().stale).toBe(false);
      });

      it('a phase change to an agent the store holds survives', async () => {
        if (!stateManager.getAgent(row.row)) stateManager.seedAgents([agentOf(row.row)]);
        update(`project.${projectId}.agent.status`, { agentId: row.row, phase: 'stopped' });
        await done();
        expect(stateManager.getAgent(row.row)?.phase).toBe('stopped');
        expect(shown(row.row)?.phase).toBe('stopped');
        expect(internals(el).agentStats).toMatchObject({
          total: row.count,
          running: row.count - 1,
        });
      });

      it('a phase change to an agent not in the store survives', async () => {
        const id = row.local ? row.row : row.offPage;
        expect(stateManager.getAgent(id)).toBeUndefined();
        update(`project.${projectId}.agent.status`, { agentId: id, phase: 'stopped' });
        await done();
        if (row.local) expect(shown(id)?.phase).toBe('stopped');
        else expect(win().memberIndex.getPhase(id)).toBe('stopped');
        expect(internals(el).agentStats).toMatchObject({
          total: row.count,
          running: row.count - 1,
        });
      });

      it('an activity change to an agent not in the store survives', async () => {
        if (row.local) {
          expect(stateManager.getAgent(row.row)).toBeUndefined();
          update(`project.${projectId}.agent.status`, { agentId: row.row, activity: 'thinking' });
          await done();
          expect(shown(row.row)?.activity).toBe('thinking');
          return;
        }
        // Off the page: an activity time that sorts onto the adopted page.
        expect(stateManager.getAgent(row.offPage)).toBeUndefined();
        update(`project.${projectId}.agent.status`, {
          agentId: row.offPage,
          activity: 'thinking',
          lastActivityEvent: row.onPageKey,
        });
        await done();
        expect(win().updatesAvailable).toBe(true);
      });

      it('a create the response predates is kept', async () => {
        const created = makeAgent(5000, { projectId, updated: page0Key });
        update(`project.${projectId}.agent.created`, { ...created, agentId: created.id });
        await done();
        expect(internals(el).agentStats.total).toBe(row.count + 1);
        if (row.local) {
          expect(shown(created.id)).toBeDefined();
        } else {
          expect(win().memberIndex.has(created.id)).toBe(true);
          expect(win().updatesAvailable).toBe(row.createLands);
        }
      });

      it('a delete leaves the agent out', async () => {
        update(`project.${projectId}.agent.deleted`, { agentId: row.row });
        await done();
        expect(shown(row.row)).toBeUndefined();
        expect(internals(el).agentStats.total).toBe(row.count - 1);
        if (!row.local) {
          // The adopted page is one row short: the backfill chip.
          expect(win().items).toHaveLength(24);
          expect(win().updatesAvailable).toBe(true);
        }
      });

      it.runIf(!row.local)('a delete off the adopted page leaves the counts', async () => {
        update(`project.${projectId}.agent.deleted`, { agentId: row.offPage });
        await done();
        expect(win().items).toHaveLength(25);
        expect(win().memberIndex.has(row.offPage)).toBe(false);
        expect(internals(el).agentStats.total).toBe(row.count - 1);
      });

      it('a resync shows the stale banner or the chip', async () => {
        reconnect();
        await done();
        if (row.local) {
          expect(win().stale).toBe(true);
          expect(win().banner?.kind).toBe('stale');
        } else {
          expect(win().updatesAvailable).toBe(true);
        }
      });
    });
  });

  describe('label typing while paged', () => {
    it('does not reset pageIndex or issue a request, and DOES apply the live preview filter to the page', async () => {
      const projectId = 'p-typing';
      localStorage.setItem('scion-view-project-agents', 'list');
      // Every 5th agent carries the env=prod label, so the current page has
      // a predictable, non-trivial filtered subset.
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, ...(i % 5 === 0 ? { labels: { env: 'prod' } } : {}) })
      );
      const requests: AgentsRequest[] = [];
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
      expect(internals(el).agentWindow.state).toBe('paged');
      await internals(el).agentWindow.next();
      await el.updateComplete;
      expect(internals(el).agentWindow.pageIndex).toBe(1);
      const itemsBefore = internals(el).agentWindow.items;
      const expectedFiltered = itemsBefore.filter((a) => a.labels?.env === 'prod').map((a) => a.id);
      expect(expectedFiltered.length).toBeGreaterThan(0);
      expect(expectedFiltered.length).toBeLessThan(itemsBefore.length);

      const before = requests.length;
      const input = labelInput(el)!;
      input.value = 'env';
      input.dispatchEvent(new Event('sl-input'));
      await el.updateComplete;

      expect(internals(el).agentWindow.pageIndex).toBe(1); // unchanged
      // The preview filter IS applied to the page.
      expect(internals(el).agentWindow.items.map((a) => a.id)).toEqual(expectedFiltered);
      expect(requests.length).toBe(before); // still zero
      const pagerEl = el.shadowRoot?.querySelector('scion-agent-pager') as unknown as {
        pageIndex: number;
        hasPrev: boolean;
      } | null;
      expect(pagerEl?.pageIndex).toBe(1);
      expect(pagerEl?.hasPrev).toBe(true);
    });
  });

  describe('a paged -> small transition resets pageIndex to 0', () => {
    function fixture60(projectId: string) {
      return Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, labels: i < 10 ? { env: 'prod' } : { env: 'dev' } })
      );
    }

    function stubAlwaysPagedUnlessLabelled(
      projectId: string,
      agents: Agent[],
      requests: AgentsRequest[]
    ) {
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          // Only the unlabelled candidate set (60) is forced paged; the
          // env=prod subset (10) fits and comes back complete.
          if (!u.searchParams.get('label')) u.searchParams.set('fit', '0');
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });
    }

    it('a k=v label commit whose set fits (paged -> small via the fit path) lands on page 0', async () => {
      const projectId = 'p-r3a1';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = fixture60(projectId);
      const requests: AgentsRequest[] = [];
      stubAlwaysPagedUnlessLabelled(projectId, agents, requests);

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      await internals(el).agentWindow.next();
      await internals(el).agentWindow.next();
      await el.updateComplete;
      expect(internals(el).agentWindow.pageIndex).toBe(2);

      const input = labelInput(el)!;
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      await el.updateComplete;

      expect(internals(el).agentWindow.state).toBe('small'); // 10 agents is below the fit threshold
      expect(internals(el).agentWindow.pageIndex).toBe(0); // reset
      expect(internals(el).agentWindow.items.length).toBe(10); // all 10 env=prod agents visible
    });

    it('a bare-key label commit (legacy path, paged -> small) lands on page 0', async () => {
      const projectId = 'p-r3a2';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = fixture60(projectId);
      const requests: AgentsRequest[] = [];
      stubAlwaysPagedUnlessLabelled(projectId, agents, requests);

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      await internals(el).agentWindow.next();
      await internals(el).agentWindow.next();
      await el.updateComplete;
      expect(internals(el).agentWindow.pageIndex).toBe(2);

      const input = labelInput(el)!;
      input.value = 'env'; // bare key: not sorted-eligible, goes through the legacy path
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      await el.updateComplete;

      expect(internals(el).agentWindow.state).toBe('small'); // legacy load, no nextCursor
      expect(internals(el).agentWindow.pageIndex).toBe(0); // reset
    });
  });

  describe('persisted page size', () => {
    it('is read once in connectedCallback, used for the first request, and matches the rendered pager', async () => {
      const projectId = 'p-window-1';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem('scion-pagesize-project-agents', '50');
      const agents = Array.from({ length: 80 }, (_, i) => makeAgent(i, { projectId }));
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
      expect(requests[0]?.url).toContain('limit=50');
      expect(internals(el).pagerPageSize).toBe(50);
      expect(internals(el).agentWindow.items.length).toBe(50);
      const pagerEl = pager(el);
      expect(pagerEl?.pageSize).toBe(50);
    });

    it('ignores an invalid stored value and falls back to the default', async () => {
      const projectId = 'p-invalid';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem('scion-pagesize-project-agents', '17');
      const agents = Array.from({ length: 10 }, (_, i) => makeAgent(i, { projectId }));
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
      expect(internals(el).pagerPageSize).toBe(25);
      expect(requests[0]?.url).toContain('limit=25');
    });
  });

  describe('stale-response guard', () => {
    it('an older trigger whose response resolves later never overwrites a newer one', async () => {
      const projectId = 'p-window-2';
      localStorage.setItem('scion-view-project-agents', 'list');
      const mountAgents = [makeAgent(0, { projectId, name: 'mount' })];
      const firstTriggerAgents = [makeAgent(1, { projectId, name: 'first-trigger' })];
      const secondTriggerAgents = [makeAgent(2, { projectId, name: 'second-trigger' })];
      const requests: AgentsRequest[] = [];

      let sortedCallCount = 0;
      const pending: Array<{ resolve: (r: Response) => void }> = [];
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          sortedCallCount++;
          const call = sortedCallCount;
          if (call === 1) {
            // The mount's own load resolves immediately, as normal.
            return createRealisticFetchHandler({
              projectId,
              projectCaps: { actions: ['read'] },
              agents: mountAgents,
              requests,
            })(input, init);
          }
          // Calls 2 and 3 (the two manual triggers below) are held open
          // until the test resolves them explicitly, out of order, with
          // the response body supplied by the test at resolve time.
          requests.push({ url: rawUrl });
          const { promise, resolve } = deferred<Response>();
          pending[call] = { resolve };
          return promise;
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents: mountAgents,
          requests,
        })(input, init);
      });

      const el = await createComponent(projectId);
      expect((el as unknown as { agents: Agent[] }).agents.map((a) => a.name)).toEqual(['mount']);

      // Trigger #1 (older), then #2 (newer), both now in flight.
      internals(el).backgroundRefresh('lifecycle-refresh');
      await vi.waitFor(() => expect(pending[2]).toBeDefined());
      internals(el).backgroundRefresh('lifecycle-refresh');
      await vi.waitFor(() => expect(pending[3]).toBeDefined());

      // Resolve the NEWER one first.
      pending[3].resolve(
        jsonResponse({
          agents: secondTriggerAgents,
          totalCount: 1,
          complete: true,
          stats: { total: 1, running: 1, agents: [[secondTriggerAgents[0].id, 'running']] },
        })
      );
      await vi.waitFor(() =>
        expect((el as unknown as { agents: Agent[] }).agents.map((a) => a.name)).toEqual([
          'second-trigger',
        ])
      );

      // Resolve the OLDER one afterward — it must be discarded.
      pending[2].resolve(
        jsonResponse({
          agents: firstTriggerAgents,
          totalCount: 1,
          complete: true,
          stats: { total: 1, running: 1, agents: [[firstTriggerAgents[0].id, 'running']] },
        })
      );
      await settle(el);
      expect((el as unknown as { agents: Agent[] }).agents.map((a) => a.name)).toEqual([
        'second-trigger',
      ]);
    });
  });

  describe('pager navigation is disabled while a page-level load is in flight', () => {
    /**
     * A Next/Prev/chip click must not race a page-level view-state request:
     * if it did, the window's own cursor stack would still belong to the
     * *old* phase/dir/label, so the resulting request would bind a stale
     * cursor to new params and the server would legitimately 400 it. The fix is to disable navigation (and the chip) outright while
     * either the window's own fetch or, while paged, a page-level load is
     * in flight, via `.loading=${agentWindow.loading || (agentWindow.state
     * === 'paged' && agentsLoading)}` — the page-level half only applies in
     * the paged state, so a held refresh never blocks small-state local
     * paging. `onPagerNav` is a defense-in-depth guard with the same
     * condition, for anything that bypasses the pager's own click handler.
     */
    function pagerLoading(el: TestEl): boolean {
      return (el.shadowRoot!.querySelector('scion-agent-pager') as unknown as { loading: boolean })
        .loading;
    }

    function clickNext(el: TestEl): void {
      // Exercises the pager's own real guard (`onNext`'s `this.loading`
      // check) rather than dispatching the bare 'next' event, which would
      // bypass that guard entirely.
      (el.shadowRoot!.querySelector('scion-agent-pager') as unknown as { onNext(): void }).onNext();
    }

    it('a Next click while a lifecycle refresh is in flight is a no-op; the refresh then lands on page 0 as intended', async () => {
      const projectId = 'p-window-3';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      let holdNextPageZeroFit = false;
      let heldResolve: ((r: Response) => void) | null = null;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // always paged
          if (holdNextPageZeroFit && !u.searchParams.has('cursor')) {
            holdNextPageZeroFit = false;
            requests.push({ url: rawUrl });
            return new Promise<Response>((resolve) => {
              heldResolve = resolve;
            });
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(pagerLoading(el)).toBe(false);

      // Start a lifecycle refresh (a page-0 fit request) and hold its response.
      holdNextPageZeroFit = true;
      internals(el).backgroundRefresh('lifecycle-refresh');
      await vi.waitFor(() => expect(heldResolve).not.toBeNull());
      await el.updateComplete;
      expect(pagerLoading(el)).toBe(true); // the pager is disabled during the gap

      const requestsBeforeClick = requests.length;
      clickNext(el); // the pager's own guard makes this a no-op
      await flushLive(el);
      expect(internals(el).agentWindow.pageIndex).toBe(0); // never navigated
      expect(requests.length).toBe(requestsBeforeClick); // no mismatched-cursor request

      // Now let the refresh finally resolve.
      heldResolve!(
        jsonResponse({
          agents: agents.slice(0, 25),
          totalCount: 30,
          complete: false,
          nextCursor: '25',
          stats: {
            total: 30,
            running: 30,
            agents: agents.map((a) => [a.id, a.phase]),
          },
        })
      );
      await settle(el);
      await el.updateComplete;

      expect(internals(el).agentWindow.pageIndex).toBe(0); // lands on page 0, as the refresh intended
      expect(pagerLoading(el)).toBe(false); // re-enabled
      // Next now works normally.
      clickNext(el);
      await settle(el);
      await el.updateComplete;
      expect(internals(el).agentWindow.pageIndex).toBe(1);
    });

    it('small-state local Next/Prev stay enabled, and work, during an in-flight lifecycle refresh', async () => {
      // Small-state pagination is a pure local slice of `display` and sends no request, so it has nothing to race with a
      // page-level load — the `.loading` gate (and `onPagerNav`'s guard)
      // only apply the page-level half in the paged state, so this isn't
      // disabled for the duration of every agent action.
      const projectId = 'p-prime-small';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      let holdNextSorted = false;
      let heldResolve: ((r: Response) => void) | null = null;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          if (holdNextSorted) {
            holdNextSorted = false;
            requests.push({ url: rawUrl });
            return new Promise<Response>((resolve) => {
              heldResolve = resolve;
            });
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small'); // 30 agents is below the fit threshold

      const requestsBeforeRefresh = requests.length;
      holdNextSorted = true;
      internals(el).backgroundRefresh('lifecycle-refresh');
      await vi.waitFor(() => expect(heldResolve).not.toBeNull());
      await el.updateComplete;
      expect(pagerLoading(el)).toBe(false); // unlike the paged state, the gate does not fire here
      expect(requests.length).toBe(requestsBeforeRefresh + 1); // the held refresh request was sent

      clickNext(el); // the pager's own real guard — must not be disabled
      await flushLive(el);
      expect(internals(el).agentWindow.pageIndex).toBe(1); // local paging worked
      expect(requests.length).toBe(requestsBeforeRefresh + 1); // Next sent nothing

      heldResolve!(
        jsonResponse({
          agents,
          totalCount: agents.length,
          complete: true,
          stats: {
            total: agents.length,
            running: agents.filter((a) => a.phase === 'running').length,
            agents: agents.map((a) => [a.id, a.phase]),
          },
        })
      );
      await settle(el);
      await el.updateComplete;

      expect(internals(el).agentWindow.state).toBe('small');
      expect(pagerLoading(el)).toBe(false);
      expect(internals(el).agentWindow.pageIndex).toBe(1); // small -> small: unaffected by the refresh
      // Paging continues to work normally once the refresh has landed.
      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onPrev(): void;
      };
      pg.onPrev();
      await settle(el);
      await el.updateComplete;
      expect(internals(el).agentWindow.pageIndex).toBe(0);
      clickNext(el);
      await settle(el);
      await el.updateComplete;
      expect(internals(el).agentWindow.pageIndex).toBe(1);
    });

    for (const kind of ['phase', 'dir', 'label', 'pagesize'] as const) {
      it(`a ${kind} change while paged disables Next until its own fit response lands, so no mismatched-cursor request is ever sent`, async () => {
        const projectId = `p-r3b-${kind}`;
        localStorage.setItem('scion-view-project-agents', 'list');
        const agents = Array.from({ length: 60 }, (_, i) =>
          makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running', labels: { env: 'dev' } })
        );
        const requests: AgentsRequest[] = [];
        let holdNextPageZeroFit = false;
        let heldResolve: ((r: Response) => void) | null = null;
        stubFetch((input: string | URL | Request, init?: RequestInit) => {
          const rawUrl =
            typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
          const u = new URL(rawUrl, 'http://localhost');
          if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
            u.searchParams.set('fit', '0'); // always paged
            if (holdNextPageZeroFit && !u.searchParams.has('cursor')) {
              holdNextPageZeroFit = false;
              requests.push({ url: rawUrl });
              return new Promise<Response>((resolve) => {
                heldResolve = resolve;
              });
            }
          }
          return createRealisticFetchHandler({
            projectId,
            projectCaps: { actions: ['read'] },
            agents,
            requests,
          })(u.toString(), init);
        });

        const el = await createComponent(projectId);
        expect(internals(el).agentWindow.state).toBe('paged');

        holdNextPageZeroFit = true;
        if (kind === 'phase') internals(el).setPhaseFilter('stopped');
        else if (kind === 'dir') internals(el).toggleSort('updated');
        else if (kind === 'label') {
          const input = labelInput(el)!;
          input.value = 'env=dev';
          input.dispatchEvent(new Event('sl-input'));
          input.dispatchEvent(new Event('sl-change'));
        } else {
          (el as unknown as { onPagerSizeChange(n: number): void }).onPagerSizeChange(50);
        }
        await vi.waitFor(() => expect(heldResolve).not.toBeNull());
        await el.updateComplete;
        expect(pagerLoading(el)).toBe(true);

        const requestsBeforeClick = requests.length;
        clickNext(el);
        await flushLive(el);
        expect(requests.length).toBe(requestsBeforeClick); // no cursor request sent at all
        expect(internals(el).agentWindow.error).toBeNull(); // in particular, no 400

        heldResolve!(
          jsonResponse({
            agents: agents.slice(0, 25),
            totalCount: 60,
            complete: false,
            nextCursor: '25',
            stats: { total: 60, running: 30, agents: agents.map((a) => [a.id, a.phase]) },
          })
        );
        await settle(el);
        await el.updateComplete;

        expect(internals(el).agentWindow.pageIndex).toBe(0); // the trigger's own page 0 landed
        expect(internals(el).agentWindow.error).toBeNull();
      });
    }
  });

  describe("a failed view-change request while paged invalidates cursors, so Next can't replay a stale one", () => {
    it('a phase change that 500s leaves the window paged with no error; Next then no-ops instead of sending a mismatched cursor', async () => {
      const projectId = 'p-triple-prime';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      let failNextSorted = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // always paged
          if (failNextSorted && !u.searchParams.has('cursor')) {
            failNextSorted = false;
            requests.push({ url: rawUrl });
            return Promise.resolve(jsonResponse({ error: { message: 'boom' } }, 500));
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.hasNext).toBe(true);

      failNextSorted = true;
      internals(el).setPhaseFilter('stopped');
      await settle(el);
      await el.updateComplete;

      // The failed view-change request keeps the previous page
      // but must no longer claim Next is possible: the stored cursor was
      // minted under the old (unfiltered) phase, and a Next now would bind
      // it to `phase=stopped` and get a 400.
      expect(internals(el).agentWindow.error).toBeNull(); // the failure itself is silent (previous data kept)
      expect(internals(el).agentWindow.hasNext).toBe(false); // invalidated

      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onNext(): void;
      };
      const requestsBeforeNext = requests.length;
      pg.onNext(); // the pager's own guard refuses: hasNext is false
      await settle(el);
      await el.updateComplete;

      expect(requests.length).toBe(requestsBeforeNext); // no mismatched-cursor request was ever sent
      expect(internals(el).agentWindow.error).toBeNull(); // in particular, no 400
      expect(internals(el).agentWindow.pageIndex).toBe(0);

      // A subsequent successful view-change restores navigation via
      // setPaged — this is a *different* phase, not a retry of 'stopped',
      // because any successful view-change restores it, not just a retry
      // of the one that failed.
      internals(el).setPhaseFilter('running');
      await settle(el);
      await el.updateComplete;
      expect(internals(el).agentWindow.error).toBeNull();
      expect(internals(el).agentWindow.hasNext).toBe(true);
    });

    it('a phase change that fails with a NETWORK error also invalidates cursors', async () => {
      const projectId = 'p-quad-prime-net';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      let failNextSorted = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // always paged
          if (failNextSorted && !u.searchParams.has('cursor')) {
            failNextSorted = false;
            requests.push({ url: rawUrl });
            return Promise.reject(new TypeError('Failed to fetch'));
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.hasNext).toBe(true);

      failNextSorted = true;
      internals(el).setPhaseFilter('stopped');
      await settle(el);
      await el.updateComplete;

      // The rejected fetch (not a non-OK response) must still invalidate:
      // a thrown network error must not fall through the catch and leave
      // the stale cursor armed.
      expect(internals(el).agentWindow.error).toBeNull();
      expect(internals(el).agentWindow.hasNext).toBe(false);

      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onNext(): void;
      };
      const requestsBeforeNext = requests.length;
      pg.onNext();
      await settle(el);
      await el.updateComplete;

      expect(requests.length).toBe(requestsBeforeNext); // no mismatched-cursor request
      expect(internals(el).agentWindow.error).toBeNull();
    });

    it('a phase change whose response is ok but json() rejects also invalidates cursors, with no unhandled rejection', async () => {
      const projectId = 'p-sorted-parse-error';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      let failNextSorted = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // always paged
          if (failNextSorted && !u.searchParams.has('cursor')) {
            failNextSorted = false;
            requests.push({ url: rawUrl });
            // ok: true, but the body is not valid JSON — response.json()
            // rejects even though the request itself succeeded (the bug
            // this test proves is fixed: that rejection used to be
            // unhandled, since response.json() ran outside a try/catch).
            return Promise.resolve(new Response('not json', { status: 200 }));
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.hasNext).toBe(true);
      const itemsBefore = internals(el).agentWindow.items;

      failNextSorted = true;
      internals(el).setPhaseFilter('stopped');
      await settle(el);
      await el.updateComplete;

      // A parse failure on an otherwise-ok response must be treated exactly
      // like the existing network-error failure exit for this path: the
      // previous page is kept and the stale cursor is invalidated exactly
      // once (not left armed, and not invalidated a second time by some
      // other path).
      expect(internals(el).agentWindow.error).toBeNull();
      expect(internals(el).agentWindow.items).toBe(itemsBefore);
      expect(internals(el).agentWindow.hasNext).toBe(false);

      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onNext(): void;
      };
      const requestsBeforeNext = requests.length;
      pg.onNext(); // the pager's own guard refuses: hasNext is false
      await settle(el);
      await el.updateComplete;

      expect(requests.length).toBe(requestsBeforeNext); // no mismatched-cursor request
      expect(internals(el).agentWindow.error).toBeNull();

      // A subsequent successful view-change still restores navigation, so
      // the invalidation above was not a permanent, leaked failure state.
      internals(el).setPhaseFilter('running');
      await settle(el);
      await el.updateComplete;
      expect(internals(el).agentWindow.error).toBeNull();
      expect(internals(el).agentWindow.hasNext).toBe(true);
    });

    it('a view-change that 422s, whose drain fallback also fails, invalidates cursors', async () => {
      const projectId = 'p-quad-prime-legacy';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      let fail422NextSorted = false;
      let failLegacy = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents`) {
          if (u.searchParams.get('sort')) {
            u.searchParams.set('fit', '0'); // always paged when sorted
            if (fail422NextSorted && !u.searchParams.has('cursor')) {
              fail422NextSorted = false;
              requests.push({ url: rawUrl });
              return Promise.resolve(
                jsonResponse({ error: { code: 'sorted_view_unavailable' } }, 422)
              );
            }
          } else if (failLegacy) {
            requests.push({ url: rawUrl });
            return Promise.resolve(jsonResponse({ error: { message: 'boom' } }, 500));
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.hasNext).toBe(true);

      // A phase change (trigger 'view-change') whose sorted request 422s;
      // the drain fallback it triggers then itself fails (500) — the
      // window must still be left with invalidated navigation, not a stale
      // cursor armed under the old (pre-change) params.
      fail422NextSorted = true;
      failLegacy = true; // every attempt, retries included
      const before = requests.length;
      internals(el).setPhaseFilter('stopped');
      // The drain retries a failed page twice (250 ms, then 500 ms).
      await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false), { timeout: 3000 });
      await el.updateComplete;
      expect(requests.length - before).toBe(1 + 3); // the 422, then three drain attempts

      expect(internals(el).agentWindow.state).toBe('paged'); // the drain failure doesn't change window state
      expect(internals(el).agentWindow.hasNext).toBe(false);
      expect(internals(el).agentWindow.hasPrev).toBe(false);

      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onNext(): void;
      };
      const requestsBeforeNext = requests.length;
      pg.onNext();
      await settle(el);
      await el.updateComplete;
      expect(requests.length).toBe(requestsBeforeNext); // no mismatched-cursor request
      expect(internals(el).agentWindow.error).toBeNull();
    });

    it('a view-change that 422s, whose drain fallback rejects with a NETWORK error, also invalidates cursors', async () => {
      const projectId = 'p-legacy-network-error';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      let fail422NextSorted = false;
      let failLegacy = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents`) {
          if (u.searchParams.get('sort')) {
            u.searchParams.set('fit', '0'); // always paged when sorted
            if (fail422NextSorted && !u.searchParams.has('cursor')) {
              fail422NextSorted = false;
              requests.push({ url: rawUrl });
              return Promise.resolve(
                jsonResponse({ error: { code: 'sorted_view_unavailable' } }, 422)
              );
            }
          } else if (failLegacy) {
            requests.push({ url: rawUrl });
            return Promise.reject(new TypeError('Failed to fetch'));
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.hasNext).toBe(true);

      // Same as the 500 case above, but the drain's fetch itself rejects
      // rather than resolving with a non-OK response.
      fail422NextSorted = true;
      failLegacy = true; // every attempt, retries included
      const before = requests.length;
      internals(el).setPhaseFilter('stopped');
      // The drain retries a failed page twice (250 ms, then 500 ms).
      await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false), { timeout: 3000 });
      await el.updateComplete;
      expect(requests.length - before).toBe(1 + 3); // the 422, then three drain attempts

      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agentWindow.hasNext).toBe(false);
      expect(internals(el).agentWindow.hasPrev).toBe(false);

      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onNext(): void;
      };
      const requestsBeforeNext = requests.length;
      pg.onNext();
      await settle(el);
      await el.updateComplete;
      expect(requests.length).toBe(requestsBeforeNext); // no mismatched-cursor request
      expect(internals(el).agentWindow.error).toBeNull();
    });
  });

  describe('a paged label commit that fails reverts the committed label on every failure path, not just a non-OK response', () => {
    it('a label commit whose sorted request rejects with a NETWORK error reverts committedLabel, so Next does not send the new label against an old cursor', async () => {
      const projectId = 'p-label-commit-network-error';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 ? 'stopped' : 'running', labels: { env: 'dev' } })
      );
      const requests: AgentsRequest[] = [];
      let failNextSorted = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // always paged
          if (failNextSorted && !u.searchParams.has('cursor')) {
            failNextSorted = false;
            requests.push({ url: rawUrl });
            return Promise.reject(new TypeError('Failed to fetch'));
          }
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).committedLabel).toBe('');
      expect(internals(el).agentWindow.hasNext).toBe(true);

      // Commit a label; its request rejects with a network error. Without
      // reverting committedLabel here (the gap this test closes), the next
      // change would resend the new label bound to the cursor minted under
      // the OLD (pre-commit) label and get a 400.
      failNextSorted = true;
      const input = labelInput(el)!;
      input.value = 'env=dev';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      await el.updateComplete;

      expect(internals(el).committedLabel).toBe(''); // reverted, not left at the rejected label
      expect(internals(el).agentWindow.state).toBe('paged'); // previous data kept
      // A label-commit failure doesn't invalidate navigation the way a
      // view-change failure does: the stored cursors are still correctly
      // bound to the label that's back in effect (the empty one), so they
      // remain usable.
      expect(internals(el).agentWindow.hasNext).toBe(true);

      const pg = el.shadowRoot!.querySelector('scion-agent-pager') as unknown as {
        onNext(): void;
      };
      const requestsBeforeNext = requests.length;
      pg.onNext();
      await settle(el);
      await el.updateComplete;
      // Exactly one legitimate request, bound to the reverted (empty)
      // label, with no 400: this is what reverting committedLabel on a
      // network error (not just a non-OK response) prevents from
      // mismatching.
      expect(requests.length).toBe(requestsBeforeNext + 1);
      expect(internals(el).agentWindow.error).toBeNull();
      expect(internals(el).agentWindow.pageIndex).toBe(1);
    });

    it('a bare-key label commit whose drain response is ok but json() rejects reverts committedLabel, with no unhandled rejection', async () => {
      const projectId = 'p-legacy-parse-error-label-commit';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 5 }, (_, i) =>
        makeAgent(i, { projectId, labels: { env: 'dev' } })
      );
      const requests: AgentsRequest[] = [];
      let failLegacy = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (
          failLegacy &&
          u.pathname === `/api/v1/projects/${projectId}/agents` &&
          !u.searchParams.get('sort')
        ) {
          requests.push({ url: rawUrl });
          // ok: true, but the body is not valid JSON — response.json()
          // rejects even though the request itself succeeded (the bug
          // this test proves is fixed: that rejection used to be
          // unhandled, since response.json() ran outside a try/catch).
          return Promise.resolve(new Response('not json', { status: 200 }));
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small'); // sorted-eligible, complete (5 is below the fit threshold)
      expect(internals(el).committedLabel).toBe('');

      // First, a successful commit on the sorted path, so the
      // revert below has a non-trivial value to land back on rather than
      // coincidentally landing on the initial empty label.
      const input = labelInput(el)!;
      input.value = 'env=dev';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      await el.updateComplete;
      expect(internals(el).committedLabel).toBe('env=dev');
      const agentsAfterFirstCommit = internals(el).agents;

      // A label with no "=" is not sorted-eligible, so this commit drains;
      // every attempt (the drain retries an unparseable body twice) fails.
      failLegacy = true;
      const requestsBeforeFailingCommit = requests.length;
      input.value = 'badlabel';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false), { timeout: 3000 });
      await el.updateComplete;

      expect(requests.length).toBe(requestsBeforeFailingCommit + 3); // one drain page, retried twice
      expect(internals(el).committedLabel).toBe('env=dev'); // reverted to the prior commit, not left at 'badlabel'
      expect(internals(el).agents).toBe(agentsAfterFirstCommit); // previous rows kept
    });
  });

  describe('fit-path label 400 restores the previous committedLabel', () => {
    it('a 400 on the sorted (fit) path keeps the previous data and reverts committedLabel', async () => {
      const projectId = 'p-fit-400';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 5 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      let failLabelOnFitPath = false;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (
          failLabelOnFitPath &&
          u.pathname === `/api/v1/projects/${projectId}/agents` &&
          u.searchParams.get('sort') &&
          u.searchParams.get('label') === 'env=prod'
        ) {
          requests.push({ url: rawUrl });
          return Promise.resolve(jsonResponse({ error: { message: 'bad label' } }, 400));
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('small'); // sorted-eligible, complete (5 is below the fit threshold)
      const before = (el as unknown as { agents: Agent[] }).agents;
      expect(before.length).toBe(5);
      expect(internals(el).committedLabel).toBe('');

      failLabelOnFitPath = true;
      const input = labelInput(el)!;
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);

      // The sorted (fit) request, not the legacy path, received the 400.
      expect(requests.some((r) => r.url.includes('sort=') && r.url.includes('label=env'))).toBe(
        true
      );
      const after = (el as unknown as { agents: Agent[] }).agents;
      expect(after.length).toBe(5); // previous data kept
      expect(internals(el).committedLabel).toBe(''); // reverted, not left at the rejected label
    });
  });

  describe('a loading indicator replaces the empty-filter message while a drain is in flight', () => {
    it('paged -> name sort shows "Loading agents…" instead of the server page while the drain is in flight, then the sorted list', async () => {
      const projectId = 'p-loading';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      let holdLegacy = false;
      let heldResolve: ((r: Response) => void) | null = null;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(rawUrl, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents` && u.searchParams.get('sort')) {
          u.searchParams.set('fit', '0'); // always paged at mount
        } else if (
          holdLegacy &&
          u.pathname === `/api/v1/projects/${projectId}/agents` &&
          !u.searchParams.get('sort')
        ) {
          holdLegacy = false;
          requests.push({ url: rawUrl });
          return new Promise<Response>((resolve) => {
            heldResolve = resolve;
          });
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(u.toString(), init);
      });

      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');

      holdLegacy = true;
      internals(el).toggleSort('name');
      await vi.waitFor(() => expect(heldResolve).not.toBeNull());
      await el.updateComplete;

      expect(el.shadowRoot?.textContent).toContain('Loading agents');
      expect(el.shadowRoot?.textContent).not.toContain('No agents match the current filter');

      heldResolve!(jsonResponse({ agents, _capabilities: { actions: ['read'] } }));
      await settle(el);
      await el.updateComplete;

      expect(el.shadowRoot?.textContent).not.toContain('Loading agents');
      expect(internals(el).agentWindow.state).toBe('small');
      expect(el.shadowRoot?.textContent).toContain('agent-0');
    });

    it('a lifecycle refresh in grid with a phase filter matching nothing shows the filter-empty message, not a loading flicker', async () => {
      const projectId = 'p-window-4';
      // Name sort is complete-needing: this.agents is populated by a drain.
      localStorage.setItem('scion-view-project-agents', 'grid');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'name', dir: 'asc' })
      );
      const agents = Array.from({ length: 5 }, (_, i) =>
        makeAgent(i, { projectId, phase: 'running' })
      );
      const requests: AgentsRequest[] = [];
      let holdNext = false;
      let heldResolve: ((r: Response) => void) | null = null;
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const rawUrl =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        if (
          holdNext &&
          rawUrl.includes(`/api/v1/projects/${projectId}/agents`) &&
          !rawUrl.includes('sort=')
        ) {
          holdNext = false;
          requests.push({ url: rawUrl });
          return new Promise<Response>((resolve) => {
            heldResolve = resolve;
          });
        }
        return createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(input, init);
      });

      const el = await createComponent(projectId);
      expect((el as unknown as { agents: Agent[] }).agents.length).toBe(5); // this.agents already holds data

      // Filter to a phase nothing matches, then trigger a lifecycle refresh
      // (held) while that filter is active — this.agents stays non-empty
      // (the OLD data) throughout the gap.
      internals(el).setPhaseFilter('stopped');
      await el.updateComplete;
      expect(el.shadowRoot?.textContent).toContain('No agents match the current filter');

      holdNext = true;
      internals(el).backgroundRefresh('lifecycle-refresh');
      await vi.waitFor(() => expect(heldResolve).not.toBeNull());
      await el.updateComplete;

      // Must NOT flicker to "Loading agents…" here — this.agents already
      // has data, it's just phase-filtered to nothing.
      expect(el.shadowRoot?.textContent).not.toContain('Loading agents');
      expect(el.shadowRoot?.textContent).toContain('No agents match the current filter');

      heldResolve!(jsonResponse({ agents, _capabilities: { actions: ['read'] } }));
      await settle(el);
      await el.updateComplete;
      expect(el.shadowRoot?.textContent).toContain('No agents match the current filter');
    });
  });

  describe('a view change while a request is in flight', () => {
    const isProjectAgents = (projectId: string) => (u: URL) =>
      u.pathname === `/api/v1/projects/${projectId}/agents`;

    function setup(projectId: string, count: number) {
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: count }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 3 === 0 ? 'stopped' : 'running' })
      );
      const requests: AgentsRequest[] = [];
      const h = holdable(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        }),
        isProjectAgents(projectId)
      );
      stubFetch(h.fn);
      return { agents, h };
    }

    const idle = async (el: TestEl) => {
      await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false));
      await settle(el);
      await el.updateComplete;
    };

    for (const change of ['phase', 'dir'] as const) {
      it(`a ${change} change during the first load supersedes it and loads the new view`, async () => {
        const projectId = `p-first-${change}`;
        const { agents, h } = setup(projectId, 60);
        h.hold();
        const el = await createComponent(projectId, { holdsFirstLoad: true });
        expect(h.sent).toHaveLength(1);
        if (change === 'phase') internals(el).setPhaseFilter('stopped');
        else internals(el).toggleSort('updated');
        h.release();
        await idle(el);

        expect(h.sent[0].signal?.aborted).toBe(true);
        expect(h.sent).toHaveLength(2);
        const q = new URL(h.sent[1].url, 'http://x').searchParams;
        if (change === 'phase') expect(q.get('phase')).toBe('stopped');
        else expect(q.get('dir')).toBe('asc');
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        const dir = change === 'dir' ? 1 : -1;
        const expected = agents
          .filter((a) => change !== 'phase' || a.phase === 'stopped')
          .sort((a, b) => dir * (a.updated ?? '').localeCompare(b.updated ?? ''))
          .slice(0, 25)
          .map((a) => a.id);
        expect(win.items.map((a) => a.id)).toEqual(expected);
      });
    }

    it('A to B to A while paged: the B request is aborted and the A page stays, with no request', async () => {
      const projectId = 'p-a-b-a';
      const { h } = setup(projectId, 60);
      const el = await createComponent(projectId);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const before = win.items.map((a) => a.id);
      h.hold();
      internals(el).toggleSort('updated'); // desc to asc
      await vi.waitFor(() => expect(h.sent).toHaveLength(2));
      internals(el).toggleSort('updated'); // back to desc
      h.release();
      await idle(el);

      expect(h.sent[1].signal?.aborted).toBe(true);
      expect(h.sent).toHaveLength(2);
      expect(win.state).toBe('paged');
      expect(win.items.map((a) => a.id)).toEqual(before);
    });

    it('a switch to name sort during the first load drains the set instead of dead-ending', async () => {
      const projectId = 'p-first-name';
      const { h } = setup(projectId, 60);
      h.hold();
      const el = await createComponent(projectId, { holdsFirstLoad: true });
      expect(h.sent).toHaveLength(1);
      internals(el).toggleSort('name');
      h.release();
      await idle(el);

      expect(h.sent[0].signal?.aborted).toBe(true);
      expect(h.sent).toHaveLength(2);
      expect(new URL(h.sent[1].url, 'http://x').searchParams.has('sort')).toBe(false);
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agentWindow.items).toHaveLength(25);
      expect(el.shadowRoot?.textContent).not.toContain('Could not load every agent');
    });

    it('a drain of 2,001 agents that lands capped after a switch to the updated sort sends one sorted request and ends paged', async () => {
      const projectId = 'p-drain-then-updated';
      const { h } = setup(projectId, 2001);
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'name', dir: 'asc' })
      );
      h.hold();
      const el = await createComponent(projectId, { holdsFirstLoad: true });
      expect(h.sent).toHaveLength(1);
      expect(new URL(h.sent[0].url, 'http://x').searchParams.has('sort')).toBe(false);
      internals(el).toggleSort('updated');
      h.release();
      await idle(el);

      // Four drain pages, then one sorted request for the updated sort.
      expect(h.sent).toHaveLength(5);
      expect(
        h.sent.slice(0, 4).every((r) => !new URL(r.url, 'http://x').searchParams.has('sort'))
      ).toBe(true);
      expect(new URL(h.sent[4].url, 'http://x').searchParams.get('sort')).toBe('updated');
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      expect(win.planRequest('view-change', '')).toBe('none');
    });

    it('a superseding sorted request aborts an in-flight Next, and its late page is not shown', async () => {
      const projectId = 'p-next-superseded';
      const { agents, h } = setup(projectId, 60);
      const el = await createComponent(projectId);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      h.hold();
      void win.next();
      await vi.waitFor(() => expect(h.sent).toHaveLength(2));
      // Next asks for the next slice of the walk order frozen from page 0.
      expect(new URL(h.sent[1].url, 'http://x').searchParams.has('ids')).toBe(true);
      internals(el).toggleSort('updated'); // desc to asc: a new sorted request
      await vi.waitFor(() => expect(h.sent).toHaveLength(3));
      expect(h.sent[1].signal?.aborted).toBe(true);
      h.release();
      await idle(el);
      expect(win.pageIndex).toBe(0);
      const expected = [...agents]
        .sort((a, b) => (a.updated ?? '').localeCompare(b.updated ?? ''))
        .slice(0, 25)
        .map((a) => a.id);
      expect(win.items.map((a) => a.id)).toEqual(expected);
    });

    for (const change of ['dir', 'phase', 'page size'] as const) {
      it(`a ${change} change on page 2 lands on page 0 with no cursor`, async () => {
        const projectId = `p-reset-${change.replace(' ', '-')}`;
        const { h } = setup(projectId, 60);
        const el = await createComponent(projectId);
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        await win.next();
        await win.next();
        expect(win.pageIndex).toBe(2);
        expect((win as unknown as { rangeStart: number }).rangeStart).toBe(50);

        if (change === 'dir') internals(el).toggleSort('updated');
        else if (change === 'phase') internals(el).setPhaseFilter('running');
        else internals(el).onPagerSizeChange(50);
        await idle(el);

        expect(win.pageIndex).toBe(0);
        expect((win as unknown as { rangeStart: number }).rangeStart).toBe(0);
        expect(new URL(h.sent.at(-1)!.url, 'http://x').searchParams.has('cursor')).toBe(false);
      });
    }
  });
}, 20_000);

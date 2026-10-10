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
 * Adopted responses keep live changes, and live creates under a label.
 *
 * One part of the "scion-page-agents — agent list window" suite. The suite is split
 * across agents-agent-window.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/agents-agent-window.ts.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Agent } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import type { AgentListWindow } from '../../client/agent-list-window.js';
import {
  fakeFetch,
  holdable,
  isGlobalAgentsList,
  makeAgent,
  type Fake,
} from './__fixtures__/global-agents-endpoint.js';
import {
  type TestEl,
  internals,
  settle,
  mountUnsettled,
  mount,
  query,
  commitLabel,
  handleUpdate,
  flushLive,
  reconnect,
  useAgentsWindowHooks,
} from './__fixtures__/agents-agent-window.js';

// Stop All asks for confirmation first; the tests confirm it.
vi.mock('../shared/confirm-dialog.js', () => ({ showConfirm: vi.fn(() => Promise.resolve(true)) }));

// Each test mounts the page and drives real fetch handling and render passes, which is slow on a
// loaded machine. The timeout is a suite option: a test's timeout is fixed when it is collected,
// so setting it from a hook would not apply.
describe('scion-page-agents — agent list window', { timeout: 30_000 }, () => {
  useAgentsWindowHooks();

  describe('every adopted response keeps the live changes that land while it is in flight', () => {
    /**
     * One way the page adopts an agents response. `start` mounts the page
     * and returns once that response's request is held; the test then
     * applies one live change, releases the request and checks the result.
     */
    interface AdoptRow {
      name: string;
      count: number;
      start(h: ReturnType<typeof holdable>): Promise<TestEl>;
      /** The adopted result is the held set (small or held), not a server page. */
      local: boolean;
      countOnly: boolean;
      /** An agent the adopted response lists (on the adopted page, or in the set). */
      row: string;
      /** Paged: a member that is not on the adopted page. */
      offPage: string;
      /** Paged: an activity time that sorts onto the adopted page. */
      onPageKey: string;
      /** Paged: a brand-new agent could land on the adopted page. */
      createLands: boolean;
      /** The server ignores sorted mode and `limit`. */
      legacy?: boolean;
    }

    const allAgents = (count: number): Agent[] =>
      Array.from({ length: count }, (_, i) => makeAgent(i));

    function holdFake(count: number, legacy = false): ReturnType<typeof holdable> {
      const fake: Fake = { agents: allAgents(count), requests: [] };
      const serve = (input: string | URL | Request, init?: RequestInit) => {
        if (!legacy) return fakeFetch(fake)(input, init);
        // An old server that ignores sorted mode and `limit`.
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        for (const k of ['sort', 'dir', 'fit', 'stats', 'limit', 'phase']) u.searchParams.delete(k);
        return fakeFetch(fake)(u.pathname + (u.search || ''), init);
      };
      const h = holdable(serve, isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      return h;
    }

    async function heldFirstLoad(h: ReturnType<typeof holdable>): Promise<TestEl> {
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      return el;
    }

    async function heldWindowFetch(
      h: ReturnType<typeof holdable>,
      fetchPage: (win: AgentListWindow) => Promise<void>
    ): Promise<TestEl> {
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('paged');
      h.hold();
      void fetchPage(internals(el).agentWindow);
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      return el;
    }

    const page0Key = '2026-01-03T00:00:00Z';
    const rows: AdoptRow[] = [
      {
        name: 'the complete fit response',
        count: 300,
        start: heldFirstLoad,
        local: true,
        countOnly: false,
        row: 'g-00007',
        offPage: '',
        onPageKey: '',
        createLands: false,
      },
      {
        name: 'the paged fit response',
        count: 1200,
        start: heldFirstLoad,
        local: false,
        countOnly: false,
        row: 'g-01190',
        offPage: 'g-00010',
        onPageKey: page0Key,
        createLands: true,
      },
      {
        name: 'a Next response',
        count: 1200,
        start: (h) => heldWindowFetch(h, (win) => win.next()),
        local: false,
        countOnly: false,
        row: 'g-01160',
        offPage: 'g-00010',
        // Page 1 holds g-01174 down to g-01150.
        onPageKey: '2026-01-02T00:00:00.01160Z',
        createLands: false,
      },
      {
        name: 'a chip refresh response',
        count: 1200,
        start: (h) => heldWindowFetch(h, (win) => win.refresh()),
        local: false,
        countOnly: false,
        row: 'g-01190',
        offPage: 'g-00010',
        onPageKey: page0Key,
        createLands: true,
      },
      {
        name: 'a drain response',
        count: 1200,
        start: (h) => {
          // A mode filter is complete-needing: the page drains.
          localStorage.setItem('scion-filter-agents-mode', 'project');
          return heldFirstLoad(h);
        },
        local: true,
        countOnly: false,
        row: 'g-00008',
        offPage: '',
        onPageKey: '',
        createLands: false,
      },
      {
        name: 'a drain that carries a legacy first page',
        count: 1200,
        legacy: true,
        start: heldFirstLoad,
        local: true,
        countOnly: false,
        row: 'g-00008',
        offPage: '',
        onPageKey: '',
        createLands: false,
      },
      {
        name: 'the count-only fit response',
        count: 2002,
        start: heldFirstLoad,
        local: false,
        countOnly: true,
        row: 'g-01990',
        offPage: 'g-00010',
        onPageKey: page0Key,
        createLands: true,
      },
      {
        name: 'a count-only Next response',
        count: 2002,
        start: (h) => heldWindowFetch(h, (win) => win.next()),
        local: false,
        countOnly: true,
        row: 'g-01960',
        offPage: 'g-00010',
        onPageKey: '2026-01-02T00:00:00.01960Z',
        createLands: false,
      },
      {
        name: 'a count-only chip refresh response',
        count: 2002,
        start: (h) => heldWindowFetch(h, (win) => win.refresh()),
        local: false,
        countOnly: true,
        row: 'g-01990',
        offPage: 'g-00010',
        onPageKey: page0Key,
        createLands: true,
      },
    ];

    const indexOf = (id: string): number => Number(id.slice(2));

    describe.each(rows)('$name', (row) => {
      let el: TestEl;
      let h: ReturnType<typeof holdable>;
      const win = (): AgentListWindow => internals(el).agentWindow;
      const shown = (id: string): Agent | undefined =>
        row.local
          ? internals(el).agents.find((a) => a.id === id)
          : win().items.find((a) => a.id === id);

      beforeEach(async () => {
        h = holdFake(row.count, row.legacy ?? false);
        el = await row.start(h);
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
        expect(win().state).toBe(row.local ? (row.count <= 500 ? 'small' : 'held') : 'paged');
        expect(win().memberIndex.countOnly).toBe(row.countOnly);
      };

      it('with no live change: no chip and no stale banner', async () => {
        await done();
        expect(win().updatesAvailable).toBe(false);
        expect(win().stale).toBe(false);
      });

      it('a phase change to an agent the store holds survives', async () => {
        const row0 = makeAgent(indexOf(row.row));
        if (!stateManager.getAgent(row.row)) stateManager.seedAgents([row0]);
        handleUpdate(`agent.${row.row}.status`, { agentId: row.row, phase: 'stopped' });
        await done();
        expect(stateManager.getAgent(row.row)?.phase).toBe('stopped');
        if (row.countOnly) {
          expect(win().updatesAvailable).toBe(true);
          return;
        }
        expect(shown(row.row)?.phase).toBe('stopped');
        expect(win().stats).toMatchObject({ total: row.count, running: row.count - 1 });
      });

      it('a phase change to an agent not in the store survives', async () => {
        const id = row.local ? row.row : row.offPage;
        expect(stateManager.getAgent(id)).toBeUndefined();
        handleUpdate(`agent.${id}.status`, { agentId: id, phase: 'stopped' });
        await done();
        if (row.countOnly) {
          expect(win().updatesAvailable).toBe(true);
          return;
        }
        if (row.local) expect(shown(id)?.phase).toBe('stopped');
        else expect(win().memberIndex.getPhase(id)).toBe('stopped');
        expect(win().stats).toMatchObject({ total: row.count, running: row.count - 1 });
      });

      it('an activity change to an agent not in the store survives', async () => {
        if (row.local) {
          expect(stateManager.getAgent(row.row)).toBeUndefined();
          handleUpdate(`agent.${row.row}.status`, { agentId: row.row, activity: 'thinking' });
          await done();
          expect(shown(row.row)?.activity).toBe('thinking');
          return;
        }
        // Off the page: an activity time that sorts onto the adopted page.
        expect(stateManager.getAgent(row.offPage)).toBeUndefined();
        handleUpdate(`agent.${row.offPage}.status`, {
          agentId: row.offPage,
          activity: 'thinking',
          lastActivityEvent: row.onPageKey,
        });
        await done();
        expect(win().updatesAvailable).toBe(true);
      });

      it('a create the response predates is kept', async () => {
        const created = makeAgent(5000);
        handleUpdate(`agent.${created.id}.created`, { ...created, agentId: created.id });
        await done();
        if (row.countOnly) {
          expect(win().updatesAvailable).toBe(true);
          return;
        }
        expect(win().stats.total).toBe(row.count + 1);
        if (row.local) {
          expect(shown(created.id)).toBeDefined();
        } else {
          expect(win().memberIndex.has(created.id)).toBe(true);
          expect(win().updatesAvailable).toBe(row.createLands);
        }
      });

      it('a delete leaves the agent out', async () => {
        handleUpdate(`agent.${row.row}.deleted`, {});
        await done();
        expect(shown(row.row)).toBeUndefined();
        if (!row.countOnly) expect(win().stats.total).toBe(row.count - 1);
        if (!row.local) {
          // The adopted page is one row short: the backfill chip.
          expect(win().items).toHaveLength(24);
          expect(win().updatesAvailable).toBe(true);
        }
      });

      it.runIf(!row.local)('a delete off the adopted page leaves the counts', async () => {
        handleUpdate(`agent.${row.offPage}.deleted`, {});
        await done();
        expect(win().items).toHaveLength(25);
        if (row.countOnly) {
          // The snapshot counts the deleted agent: the chip.
          expect(win().updatesAvailable).toBe(true);
          return;
        }
        expect(win().memberIndex.has(row.offPage)).toBe(false);
        expect(win().stats.total).toBe(row.count - 1);
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

  describe('live creates under a committed label', () => {
    it('a live create during a labelled scope-all fit request joins only if it matches the label', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 30 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('small');
      h.hold();
      commitLabel(el, 'env=prod');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      expect(query(h.sent.at(-1)!.url).get('label')).toBe('env=prod');
      expect(query(h.sent.at(-1)!.url).get('fit')).toBe('500');
      handleUpdate('agent.new-prod.created', {
        ...makeAgent(6000),
        id: 'new-prod',
        agentId: 'new-prod',
      });
      handleUpdate('agent.new-dev.created', {
        ...makeAgent(6001, { labels: { env: 'dev' } }),
        id: 'new-dev',
        agentId: 'new-dev',
      });
      (stateManager as unknown as { flush(): void }).flush();
      h.release();
      await settle(el);
      const ids = new Set(internals(el).agents.map((a) => a.id));
      expect(ids.has('new-prod')).toBe(true);
      expect(ids.has('new-dev')).toBe(false);
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agentWindow.stats.total).toBe(31);
      expect(internals(el).agentWindow.display.every((a) => a.labels?.env === 'prod')).toBe(true);
    });
  });
});

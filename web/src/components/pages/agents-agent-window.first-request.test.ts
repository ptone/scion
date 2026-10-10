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
 * The first request, fallbacks, view changes, completeness and reuse.
 *
 * One part of the "scion-page-agents — agent list window" suite. The suite is split
 * across agents-agent-window.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/agents-agent-window.ts.
 */

import { describe, it, expect, vi } from 'vitest';
import type { Agent } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import { AgentDrainRunner } from '../../client/agent-drain.js';
import {
  SCOPE_CAPS,
  fakeFetch,
  holdable,
  isGlobalAgentsList,
  isMine,
  isShared,
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
  unmount,
  text,
  commitLabel,
  setView,
  pager,
  handleUpdate,
  flushLive,
  reconnect,
  stubFake,
  useAgentsWindowHooks,
} from './__fixtures__/agents-agent-window.js';

// Stop All asks for confirmation first; the tests confirm it.
vi.mock('../shared/confirm-dialog.js', () => ({ showConfirm: vi.fn(() => Promise.resolve(true)) }));

// Each test mounts the page and drives real fetch handling and render passes, which is slow on a
// loaded machine. The timeout is a suite option: a test's timeout is fixed when it is collected,
// so setting it from a hook would not apply.
describe('scion-page-agents — agent list window', { timeout: 30_000 }, () => {
  useAgentsWindowHooks();

  describe('the first request', () => {
    it('is one sorted fit request with stats; the scope is sent only when not all, the label only with =, and the phase when set', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-filter-agents-phase', 'running');
      const el = await mount();
      expect(fake.requests.length).toBe(1);
      const q = new URL(fake.requests[0], 'http://x').searchParams;
      expect(q.get('sort')).toBe('updated');
      expect(q.get('dir')).toBe('desc');
      expect(q.get('limit')).toBe('25');
      expect(q.get('fit')).toBe('500');
      expect(q.get('stats')).toBe('1');
      expect(q.get('phase')).toBe('running');
      expect(q.has('scope')).toBe(false);
      expect(q.has('label')).toBe(false);

      commitLabel(el, 'env');
      await settle(el);
      // A bare key is a complete-needing label: a drain, sent without the label.
      const drainQ = new URL(fake.requests.at(-1)!, 'http://x').searchParams;
      expect(drainQ.has('sort')).toBe(false);
      expect(drainQ.has('label')).toBe(false);
      commitLabel(el, 'env=prod');
      await settle(el);
      expect(new URL(fake.requests.at(-1)!, 'http://x').searchParams.get('label')).toBe('env=prod');
      internals(el).setScope('mine');
      await settle(el);
      expect(new URL(fake.requests.at(-1)!, 'http://x').searchParams.get('scope')).toBe('mine');
    });

    it('a complete response renders every agent with the scope capabilities; above 500 the first page is shown', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 30 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agentWindow.stats.total).toBe(30);
      expect(el.shadowRoot?.querySelectorAll('tbody tr').length).toBe(25);
      expect(text(el)).toContain('New Agent');

      unmount(el);
      stateManager.setScope({ type: 'brokers-list' });
      fake.agents = Array.from({ length: 1200 }, (_, i) => makeAgent(i));
      fake.requests.length = 0;
      const el2 = await mount();
      expect(fake.requests.length).toBe(1);
      expect(internals(el2).agentWindow.state).toBe('paged');
      expect(internals(el2).agents).toEqual([]);
      expect(internals(el2).agentWindow.stats.total).toBe(1200);
      expect(el2.shadowRoot?.querySelectorAll('tbody tr').length).toBe(25);
      expect(internals(el2).agentWindow.items[0].id).toBe('g-01199');
    });

    it('a legacy server (no sorted mode) that ignores limit is drained from its first page', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const legacy = (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        for (const k of ['sort', 'dir', 'fit', 'stats', 'limit', 'phase']) u.searchParams.delete(k);
        return fakeFetch(fake)(u.pathname + (u.search || ''), init);
      };
      vi.stubGlobal('fetch', legacy);
      const el = await mount();
      // The drain continues from the legacy first page and its cursor:
      // the first request plus pages 2 and 3, with no refetch of page 1.
      expect(fake.requests.length).toBe(3);
      expect(new URL(fake.requests[1], 'http://localhost').searchParams.get('cursor')).toBe('500');
      expect(internals(el).agentWindow.state).toBe('held');
      expect(internals(el).agents.length).toBe(1200);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
    });

    /** An old server that ignores sorted mode and `limit`, held until the test releases it. */
    function heldLimitIgnoringLegacy(fake: Fake): ReturnType<typeof holdable> {
      const legacy = (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        for (const k of ['sort', 'dir', 'fit', 'stats', 'limit', 'phase']) u.searchParams.delete(k);
        return fakeFetch(fake)(u.pathname + (u.search || ''), init);
      };
      const h = holdable(legacy, isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      return h;
    }

    it('a live phase change to a row of a carried legacy first page survives the drain', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = heldLimitIgnoringLegacy(fake);
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      // g-00007 is on the held first page, which still says running.
      handleUpdate('agent.g-00007.status', { agentId: 'g-00007', phase: 'stopped' });
      await flushLive(el);
      h.release();
      await settle(el);
      expect(fake.requests.length).toBe(3);
      expect(new URL(fake.requests[1], 'http://localhost').searchParams.get('cursor')).toBe('500');
      expect(internals(el).agentWindow.state).toBe('held');
      expect(stateManager.getAgent('g-00007')?.phase).toBe('stopped');
      expect(internals(el).agents.find((a) => a.id === 'g-00007')?.phase).toBe('stopped');
      expect(internals(el).agentWindow.stats).toMatchObject({ total: 1200, running: 1199 });
    });

    it('a full legacy first page with one row deleted live is still carried', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 2000 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = heldLimitIgnoringLegacy(fake);
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      // The server's answer still lists g-00007, which this client saw deleted.
      handleUpdate('agent.g-00007.deleted', {});
      await flushLive(el);
      h.release();
      await settle(el);
      // The first request plus pages from cursors 500, 1000 and 1500.
      expect(fake.requests.length).toBe(4);
      expect(
        fake.requests.slice(1).map((u) => new URL(u, 'http://localhost').searchParams.get('cursor'))
      ).toEqual(['500', '1000', '1500']);
      expect(internals(el).agents.some((a) => a.id === 'g-00007')).toBe(false);
      expect(internals(el).agents.length).toBe(1999);
    });

    it('a phase filter change during a carrying legacy drain neither aborts it nor sends a request', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = heldLimitIgnoringLegacy(fake);
      h.hold(2);
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      h.release();
      await vi.waitFor(() => expect(h.sent).toHaveLength(2));
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      // Drain page 2 is held. The drain carries no sorted request, so a
      // phase filter change (local once held) has nothing to supersede.
      internals(el).setPhaseFilter('stopped');
      await el.updateComplete;
      h.release();
      await settle(el);
      expect(h.sent.some((r) => r.signal?.aborted)).toBe(false);
      expect(h.sent).toHaveLength(3);
      expect(internals(el).agentWindow.state).toBe('held');
    });

    /** An old server: no sorted mode, but `limit` is honoured. */
    const limitHonouringLegacy =
      (fake: Fake) => (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        for (const k of ['sort', 'dir', 'fit', 'stats', 'phase']) u.searchParams.delete(k);
        return fakeFetch(fake)(u.pathname + (u.search || ''), init);
      };

    it('1,600 agents at page size 25 on a legacy server that honours limit: the short answer is discarded, and four drain pages end held and complete', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1600 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      vi.stubGlobal('fetch', limitHonouringLegacy(fake));
      const el = await mount();
      expect(query(fake.requests[0]).get('limit')).toBe('25');
      // The 25-row answer, then four drain pages of 500 from the start.
      expect(fake.requests).toHaveLength(5);
      expect(fake.requests.slice(1).map((r) => query(r).get('cursor'))).toEqual([
        null,
        '500',
        '1000',
        '1500',
      ]);
      expect(fake.requests.slice(1).every((r) => query(r).get('limit') === '500')).toBe(true);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('held');
      expect(win.banner).toBeNull();
      expect(internals(el).agents).toHaveLength(1600);
      expect(new Set(internals(el).agents.map((a) => a.id)).size).toBe(1600);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      expect(text(el)).not.toContain('more exist');
    });

    it('a row only the discarded short answer holds never reaches the agents, the store or the counts', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1600 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const inner = limitHonouringLegacy(fake);
      // The short answer still lists an agent the server no longer returns.
      const gone = makeAgent(9999, { id: 'gone-1', name: 'gone-1' });
      const withGoneRow = async (input: string | URL | Request, init?: RequestInit) => {
        const res = await inner(input, init);
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        if (!isGlobalAgentsList(new URL(raw, 'http://localhost'))) return res;
        if (query(raw).get('limit') !== '25') return res;
        const body = (await res.clone().json()) as { agents: Agent[] };
        return new Response(JSON.stringify({ ...body, agents: [gone, ...body.agents] }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      };
      vi.stubGlobal('fetch', withGoneRow);
      const el = await mount();
      expect(fake.requests).toHaveLength(5);
      expect(internals(el).agentWindow.state).toBe('held');
      expect(internals(el).agents.some((a) => a.id === 'gone-1')).toBe(false);
      expect(internals(el).agents).toHaveLength(1600);
      expect(stateManager.getAgent('gone-1')).toBeUndefined();
      expect(internals(el).agentWindow.stats.total).toBe(1600);
    });

    it('2,100 agents at page size 25 on a legacy server that honours limit end capped in five requests with exactly the newest 2,000', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 2100 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      vi.stubGlobal('fetch', limitHonouringLegacy(fake));
      const el = await mount();
      expect(fake.requests).toHaveLength(5);
      expect(fake.requests.slice(1).map((r) => query(r).get('cursor'))).toEqual([
        null,
        '500',
        '1000',
        '1500',
      ]);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('capped');
      expect(win.banner?.kind).toBe('capped');
      expect(internals(el).agents).toHaveLength(2000);
      expect(internals(el).agents.map((a) => a.id)).toEqual(
        fake.agents.slice(0, 2000).map((a) => a.id)
      );
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      expect(text(el)).toContain('2,000 loaded (newest 2,000 checked), more exist');
    });
  });

  describe('a legacy fallback and drain failures', () => {
    it('a legacy server that honours phase answers a phased first request with part of the set: the whole set is drained, not marked complete from that page', async () => {
      const agents = Array.from({ length: 1200 }, (_, i) =>
        makeAgent(i, { phase: i % 3 === 0 ? 'stopped' : 'running' })
      );
      const requests: string[] = [];
      const full: Fake = { agents, requests };
      const stopped: Fake = { agents: agents.filter((a) => a.phase === 'stopped'), requests };
      const legacy = (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        const phase = u.searchParams.get('phase');
        for (const k of ['sort', 'dir', 'fit', 'stats', 'limit', 'phase']) u.searchParams.delete(k);
        const target = phase === 'stopped' ? stopped : full;
        return fakeFetch(target)(u.pathname + (u.search || ''), init);
      };
      vi.stubGlobal('fetch', legacy);
      localStorage.setItem('scion-filter-agents-phase', 'stopped');
      const el = await mount();
      // The phased first request, then three unphased legacy pages.
      expect(requests.length).toBe(1 + 3);
      expect(query(requests[1]).has('cursor')).toBe(false);
      expect(query(requests[1]).has('phase')).toBe(false);
      expect(internals(el).agents).toHaveLength(1200);
      expect(internals(el).agentWindow.state).toBe('held');
      expect(internals(el).agentWindow.total).toBe(400);
    });

    it('a full phased legacy first page with a nextCursor is not carried: the whole set is drained from the start without the phase', async () => {
      // 600 of 1,800 stopped, so the phased first page is a full 500-row page.
      const agents = Array.from({ length: 1800 }, (_, i) =>
        makeAgent(i, { phase: i % 3 === 0 ? 'stopped' : 'running' })
      );
      const requests: string[] = [];
      const full: Fake = { agents, requests };
      const stopped: Fake = { agents: agents.filter((a) => a.phase === 'stopped'), requests };
      const sentPhases: Array<string | null> = [];
      // An old server: no sorted mode, `phase` honoured, `limit` ignored.
      const legacy = (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        sentPhases.push(u.searchParams.get('phase'));
        const phase = u.searchParams.get('phase');
        for (const k of ['sort', 'dir', 'fit', 'stats', 'limit', 'phase']) u.searchParams.delete(k);
        const target = phase === 'stopped' ? stopped : full;
        return fakeFetch(target)(u.pathname + (u.search || ''), init);
      };
      vi.stubGlobal('fetch', legacy);
      localStorage.setItem('scion-filter-agents-phase', 'stopped');
      const el = await mount();
      // The phased first request, then four unphased drain pages from the start.
      expect(sentPhases).toEqual(['stopped', null, null, null, null]);
      expect(requests).toHaveLength(1 + 4);
      expect(requests.slice(1).map((r) => query(r).get('cursor'))).toEqual([
        null,
        '500',
        '1000',
        '1500',
      ]);
      expect(internals(el).agents).toHaveLength(1800);
      expect(internals(el).agentWindow.state).toBe('held');
      expect(internals(el).agentWindow.total).toBe(600);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
    });

    it('an empty readable first drain page then a failing page is an incomplete set, not the error path', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const inner = fakeFetch(fake);
      vi.stubGlobal('fetch', async (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        if (isGlobalAgentsList(u) && !u.searchParams.has('sort')) {
          fake.requests.push(raw);
          if (!u.searchParams.has('cursor')) {
            return new Response(
              JSON.stringify({ agents: [], nextCursor: '500', _capabilities: SCOPE_CAPS }),
              { status: 200, headers: { 'Content-Type': 'application/json' } }
            );
          }
          return new Response('{}', { status: 502 });
        }
        return inner(input, init);
      });
      localStorage.setItem('scion-filter-agents-mode', 'project');
      const el = await mount();
      expect(internals(el).error).toBeNull();
      expect(internals(el).agentWindow.state).toBe('capped');
      expect(internals(el).agentWindow.banner?.kind).toBe('failed');
      expect(text(el)).toContain('Incomplete: loaded 0');
    });
  });

  describe('full-view pages replace stored agents', () => {
    it('a field missing from a later full-view page is gone from the store', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i, { taskSummary: 'old task' })),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('paged');
      const id = internals(el).agentWindow.items[0].id;
      expect(stateManager.getAgent(id)?.taskSummary).toBe('old task');
      fake.agents = fake.agents.map((a) => {
        const { taskSummary: _dropped, ...rest } = a;
        return rest as Agent;
      });
      await internals(el).agentWindow.refresh();
      await el.updateComplete;
      expect(stateManager.getAgent(id)).toBeDefined();
      expect(stateManager.getAgent(id)?.taskSummary).toBeUndefined();
    });
  });

  describe('a view change while a request is in flight', () => {
    const phased = (n: number) =>
      Array.from({ length: n }, (_, i) =>
        makeAgent(i, { phase: i % 3 === 0 ? 'stopped' : 'running' })
      );
    const byUpdated = (dir: 'asc' | 'desc') => (a: Agent, b: Agent) =>
      (dir === 'asc' ? 1 : -1) * (a.updated ?? '').localeCompare(b.updated ?? '');

    for (const change of ['phase', 'dir'] as const) {
      it(`a ${change} change during the first load supersedes it and loads the new view`, async () => {
        const fake: Fake = { agents: phased(1200), requests: [] };
        const h = holdable(fakeFetch(fake), isGlobalAgentsList);
        h.hold();
        vi.stubGlobal('fetch', h.fn);
        const el = await mountUnsettled();
        await vi.waitFor(() => expect(h.sent).toHaveLength(1));
        if (change === 'phase') internals(el).setPhaseFilter('stopped');
        else internals(el).toggleSort('updated');
        h.release();
        await settle(el);

        expect(h.sent[0].signal?.aborted).toBe(true);
        expect(h.sent).toHaveLength(2);
        const q = query(h.sent[1].url);
        if (change === 'phase') expect(q.get('phase')).toBe('stopped');
        else expect(q.get('dir')).toBe('asc');
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        expect(win.planRequest('view-change', '')).toBe('none');
        const expected = fake.agents
          .filter((a) => change !== 'phase' || a.phase === 'stopped')
          .sort(byUpdated(change === 'dir' ? 'asc' : 'desc'))
          .slice(0, 25)
          .map((a) => a.id);
        expect(win.items.map((a) => a.id)).toEqual(expected);
      });
    }

    it('A to B to A while paged: the B request is aborted and the A page stays, with no request', async () => {
      const fake: Fake = { agents: phased(1200), requests: [] };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const before = win.items.map((a) => a.id);
      h.hold();
      internals(el).toggleSort('updated'); // desc to asc
      await vi.waitFor(() => expect(h.sent).toHaveLength(2));
      internals(el).toggleSort('updated'); // back to desc
      h.release();
      await settle(el);

      expect(h.sent[1].signal?.aborted).toBe(true);
      expect(h.sent).toHaveLength(2);
      expect(win.state).toBe('paged');
      expect(win.items.map((a) => a.id)).toEqual(before);
      expect(win.planRequest('view-change', '')).toBe('none');
    });

    it('a switch to name sort during the first load drains the set instead of dead-ending', async () => {
      const fake: Fake = { agents: phased(1200), requests: [] };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      h.hold();
      vi.stubGlobal('fetch', h.fn);
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.sent).toHaveLength(1));
      internals(el).toggleSort('name');
      h.release();
      await settle(el);

      expect(h.sent[0].signal?.aborted).toBe(true);
      // The aborted fit request, then the three legacy drain pages.
      expect(h.sent).toHaveLength(4);
      expect(query(h.sent[1].url).has('sort')).toBe(false);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('held');
      expect(win.items).toHaveLength(25);
      expect(text(el)).not.toContain('Could not load every agent');
      expect(text(el)).not.toContain('Loading agents');
    });
  });

  describe('the window page fetch on a paged set', () => {
    async function nextInFlight(): Promise<{
      el: TestEl;
      h: ReturnType<typeof holdable>;
      fake: Fake;
    }> {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('paged');
      h.hold();
      void internals(el).agentWindow.next();
      await vi.waitFor(() => expect(h.sent).toHaveLength(2));
      // Next asks for the next slice of the walk order frozen from page 0.
      expect(query(h.sent[1].url).get('ids')?.split(',')).toHaveLength(25);
      expect(h.sent[1].signal?.aborted).toBe(false);
      return { el, h, fake };
    }

    it('disconnecting the page aborts an in-flight Next', async () => {
      const { el, h } = await nextInFlight();
      unmount(el);
      expect(h.sent[1].signal?.aborted).toBe(true);
      h.release();
    });

    it('a superseding sorted request aborts an in-flight Next, and its late page is not shown', async () => {
      const { el, h } = await nextInFlight();
      internals(el).toggleSort('updated'); // desc to asc: a new sorted request
      await vi.waitFor(() => expect(h.sent).toHaveLength(3));
      expect(h.sent[1].signal?.aborted).toBe(true);
      expect(query(h.sent[2].url).get('dir')).toBe('asc');
      h.release();
      await settle(el);
      const win = internals(el).agentWindow;
      expect(win.pageIndex).toBe(0);
      expect(win.items[0].id).toBe('g-00000');
    });
  });

  describe('a view change while a drain is in flight', () => {
    /** Mounts 2,001 agents in the tree view with the first drain page held, and switches to the list view. */
    async function switchDuringDrain(
      fail: boolean
    ): Promise<{ el: TestEl; sent: Array<{ url: string }> }> {
      const fake: Fake = {
        agents: Array.from({ length: 2001 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const inner = fakeFetch(fake);
      const failing = (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        if (
          fail &&
          isGlobalAgentsList(new URL(raw, 'http://localhost')) &&
          query(raw).has('sort')
        ) {
          return Promise.resolve(new Response('{}', { status: 500 }));
        }
        return inner(input, init);
      };
      const h = holdable(failing, isGlobalAgentsList);
      h.hold();
      vi.stubGlobal('fetch', h.fn);
      localStorage.setItem('scion-view-agents', 'graph');
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.sent).toHaveLength(1));
      expect(query(h.sent[0].url).has('sort')).toBe(false);
      setView(el, 'list');
      await el.updateComplete;
      h.release();
      await settle(el);
      return { el, sent: h.sent };
    }

    it('a drain that lands capped after a switch to an eligible view sends one sorted request and ends paged', async () => {
      const { el, sent } = await switchDuringDrain(false);
      // Four drain pages, then one sorted request for the list view.
      expect(sent).toHaveLength(5);
      expect(sent.slice(0, 4).every((r) => !query(r.url).has('sort'))).toBe(true);
      expect(query(sent[4].url).get('sort')).toBe('updated');
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      expect(win.planRequest('view-change', '')).toBe('none');
      expect(internals(el).error).toBeNull();
    });

    it('a failed sorted request after the drain landed keeps the drained rows with no page error', async () => {
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const { el, sent } = await switchDuringDrain(true);
      expect(sent).toHaveLength(5);
      expect(query(sent[4].url).has('sort')).toBe(true);
      const win = internals(el).agentWindow;
      expect(internals(el).error).toBeNull();
      expect(win.state).toBe('capped');
      expect(internals(el).agents).toHaveLength(2000);
      expect(el.shadowRoot?.querySelectorAll('tbody tr').length).toBe(25);
      expect(el.shadowRoot?.querySelector('.error-details')).toBeNull();
      expect(warn).toHaveBeenCalled();
    });
  });

  describe('the completeness flag and the reuse branch', () => {
    it('an unlabelled scope-all complete load sets full; revisiting /agents then issues no request', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      unmount(el);
      const el2 = await mount();
      expect(fake.requests.length).toBe(1);
      expect(internals(el2).agentWindow.state).toBe('small');
      expect(internals(el2).agentWindow.display.length).toBe(25);
      expect(text(el2)).toContain('New Agent');
      // A lifecycle refresh after the reuse costs one request, as today.
      internals(el2).backgroundRefresh('lifecycle-refresh');
      await settle(el2);
      expect(fake.requests.length).toBe(2);
    });

    it('a label commit after a complete load keeps the flag, and /agents then issues no request', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      commitLabel(el, 'env=prod');
      await settle(el);
      expect(fake.requests.length).toBe(2);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      unmount(el);
      const el2 = await mount();
      expect(fake.requests.length).toBe(2);
      expect(internals(el2).committedLabel).toBe('');
      expect(internals(el2).agentWindow.display.length).toBe(25);
    });

    it('mine, then a label, then all with the label still committed: no flag; a dashboard-scope page in between, then /agents issues one unlabelled load that sets it', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-scope-agents', 'mine');
      const el = await mount();
      expect(fake.requests.length).toBe(1);
      commitLabel(el, 'env=prod');
      await settle(el);
      internals(el).setScope('all');
      await settle(el);
      expect(fake.requests.length).toBe(3);
      expect(new URL(fake.requests[2], 'http://x').searchParams.get('label')).toBe('env=prod');
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      unmount(el);

      // A dashboard-scope page that loads no agents (the projects list).
      stateManager.setScope({ type: 'dashboard' });
      const el2 = await mount();
      expect(fake.requests.length).toBe(4);
      const q = new URL(fake.requests[3], 'http://x').searchParams;
      expect(q.has('scope')).toBe(false);
      expect(q.has('label')).toBe(false);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      expect(internals(el2).agentWindow.display.length).toBe(25);
    });

    it('a compact flag does not satisfy the reuse branch', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      stateManager.setScope({ type: 'dashboard' });
      stateManager.seedAgents(fake.agents);
      stateManager.seedScopeCapabilities('agent', SCOPE_CAPS);
      stateManager.markAgentSetComplete('compact');
      const el = await mount();
      expect(fake.requests.length).toBe(1);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      expect(internals(el).agentWindow.display.length).toBe(25);
    });

    it('the flag is not set by a paged, labelled, mine or shared load, and none of them clears it', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-scope-agents', 'shared');
      const el = await mount();
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      commitLabel(el, 'env=prod');
      await settle(el);
      internals(el).setScope('all');
      await settle(el);
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      commitLabel(el, '');
      await settle(el);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);

      internals(el).setScope('mine');
      await settle(el);
      commitLabel(el, 'env=prod');
      await settle(el);
      internals(el).setScope('all');
      await settle(el);
      fake.agents = Array.from({ length: 1200 }, (_, i) => makeAgent(i));
      commitLabel(el, '');
      await settle(el);
      expect(internals(el).agentWindow.state).toBe('paged');
      await internals(el).agentWindow.next();
      await settle(el);
      const n = fake.requests.length;
      expect(pager(el).showChip).toBe(false);
      reconnect();
      await settle(el);
      expect(pager(el).showChip).toBe(true);
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      expect(fake.requests.length).toBe(n);
    });

    it('a reconnect of the live connection in the held state shows the stale banner, keeps the flag and sends nothing', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-view-agents', 'graph');
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('held');
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      expect(internals(el).agentWindow.banner).toBeNull();
      const n = fake.requests.length;
      reconnect();
      await settle(el);
      expect(internals(el).agentWindow.banner?.kind).toBe('stale');
      expect(text(el)).toContain('may be stale');
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
      expect(fake.requests.length).toBe(n);
    }, 30_000);

    it('a paged first load does not set the flag; a drain that completes does', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      setView(el, 'graph');
      await settle(el);
      expect(internals(el).agentWindow.state).toBe('held');
      expect(stateManager.isAgentSetComplete('full')).toBe(true);
    });
  });

  describe('complete-needing views', () => {
    it('a mode filter while paged drains once, then filters the held set locally', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      internals(el).setModeFilter('branch');
      await settle(el);
      expect(fake.requests.length).toBe(1 + 3);
      expect(internals(el).agentWindow.state).toBe('held');
      expect(internals(el).agentWindow.total).toBe(600);
      expect(internals(el).agentWindow.items.every((a) => a.messageMode === 'branch')).toBe(true);
      internals(el).setModeFilter('');
      await settle(el);
      expect(fake.requests.length).toBe(4);
    });

    it('a persisted mode filter drains from the first request', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-filter-agents-mode', 'project');
      const el = await mount();
      expect(fake.requests.length).toBe(3);
      expect(new URL(fake.requests[0], 'http://x').searchParams.has('sort')).toBe(false);
      expect(internals(el).agentWindow.total).toBe(600);
    });

    it('a held mine set does not add a live create, marks the set as possibly stale, and issues no request', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-scope-agents', 'mine');
      localStorage.setItem('scion-view-agents', 'graph');
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('held');
      const n = fake.requests.length;
      expect(internals(el).agentWindow.banner).toBeNull();
      handleUpdate('agent.new-1.created', { ...makeAgent(5000), id: 'new-1', agentId: 'new-1' });
      await flushLive(el);
      expect(internals(el).agents.some((a) => a.id === 'new-1')).toBe(false);
      expect(internals(el).agentWindow.banner?.kind).toBe('stale');
      expect(text(el)).toContain('may be stale');
      expect(fake.requests.length).toBe(n);
    });

    for (const scope of ['mine', 'shared'] as const) {
      it(`a live create during an in-flight ${scope} drain is not added and marks the set stale, with no request`, async () => {
        const fake: Fake = {
          agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
          requests: [],
        };
        const inner = fakeFetch(fake);
        let armed = false;
        vi.stubGlobal('fetch', async (input: string | URL | Request, init?: RequestInit) => {
          const raw =
            typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
          const u = new URL(raw, 'http://localhost');
          if (armed && isGlobalAgentsList(u) && !u.searchParams.has('sort')) {
            // The first legacy page of the drain: a create lands and is
            // flushed before the page replies.
            armed = false;
            handleUpdate('agent.new-7.created', {
              ...makeAgent(5001),
              id: 'new-7',
              agentId: 'new-7',
            });
            (stateManager as unknown as { flush(): void }).flush();
          }
          return inner(input, init);
        });
        localStorage.setItem('scion-view-agents', 'graph');
        const el = await mount();
        expect(internals(el).agentWindow.state).toBe('held');
        const before = fake.requests.length;
        armed = true;
        internals(el).setScope(scope);
        await settle(el);
        await flushLive(el);
        const members = fake.agents.filter(scope === 'mine' ? isMine : isShared).length;
        expect(fake.requests.length).toBe(before + Math.ceil(members / 500));
        expect(internals(el).agents).toHaveLength(members);
        expect(internals(el).agents.some((a) => a.id === 'new-7')).toBe(false);
        expect(internals(el).agentWindow.banner?.kind).toBe('stale');
        expect(text(el)).toContain('may be stale');
        expect(el.shadowRoot?.querySelector('.agent-window-banner')).not.toBeNull();
      }, 30_000);
    }
  });

  describe('leaving the page during a drain', () => {
    it('leaving the page during a drain aborts its page fetch and sends no further drain page', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      h.hold();
      vi.stubGlobal('fetch', h.fn);
      localStorage.setItem('scion-view-agents', 'graph');
      const run = vi.spyOn(AgentDrainRunner.prototype, 'run');
      const el = await mountUnsettled();
      // The tree view drains from the first request.
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      expect(h.sent).toHaveLength(1);
      expect(query(h.sent[0].url).has('sort')).toBe(false);
      expect(h.sent[0].signal?.aborted).toBe(false);

      // Leaving /agents for another dashboard page keeps the store's scope,
      // so only the page itself can stop the drain.
      unmount(el);
      expect(h.sent[0].signal?.aborted).toBe(true);
      h.release();
      // Wait until the aborted drain run has settled with no result.
      expect(run).toHaveBeenCalledTimes(1);
      let outcome: unknown = 'pending';
      void (run.mock.results[0].value as Promise<unknown>).then((r) => (outcome = r));
      await vi.waitFor(() => expect(outcome).toBeNull(), { timeout: 10_000 });
      await el.updateComplete;
      expect(run).toHaveBeenCalledTimes(1);
      expect(h.sent).toHaveLength(1);
      expect(stateManager.getAgents()).toHaveLength(0);
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
    });
  });

  describe('live changes to agents not in the store while a paged request is in flight', () => {
    function heldFake(length: number): { fake: Fake; h: ReturnType<typeof holdable> } {
      const fake: Fake = {
        agents: Array.from({ length }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      return { fake, h };
    }

    it('an off-page phase change while the paged fit request is in flight reaches the running count', async () => {
      const { h } = heldFake(1200);
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      expect(stateManager.getAgent('g-00010')).toBeUndefined();
      handleUpdate('agent.g-00010.status', { agentId: 'g-00010', phase: 'stopped' });
      await flushLive(el);
      h.release();
      await settle(el);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      expect(win.items.some((a) => a.id === 'g-00010')).toBe(false);
      expect(win.stats.total).toBe(1200);
      expect(win.stats.running).toBe(1199);
      expect(win.memberIndex.getPhase('g-00010')).toBe('stopped');
    });

    it('an off-page agent changed while unknown, then created and changed again, counts with its latest phase', async () => {
      const { h } = heldFake(1200);
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      handleUpdate('agent.g-00010.status', { agentId: 'g-00010', phase: 'stopped' });
      await flushLive(el);
      handleUpdate('agent.g-00010.created', { ...makeAgent(10), agentId: 'g-00010' });
      handleUpdate('agent.g-00010.status', { agentId: 'g-00010', phase: 'running' });
      await flushLive(el);
      expect(stateManager.getAgent('g-00010')?.phase).toBe('running');
      h.release();
      await settle(el);
      const win = internals(el).agentWindow;
      expect(win.memberIndex.getPhase('g-00010')).toBe('running');
      expect(win.stats.running).toBe(1200);
    });

    it('an off-page activity bump during a held Next raises the chip', async () => {
      const { h } = heldFake(1200);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      h.hold();
      void win.next();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      expect(query(h.sent.at(-1)!.url).get('ids')?.split(',')).toHaveLength(25);
      // Page 1 holds g-01174 down to g-01150; this activity time sorts inside it.
      handleUpdate('agent.g-00010.status', {
        agentId: 'g-00010',
        lastActivityEvent: '2026-01-02T00:00:00.01160Z',
      });
      await flushLive(el);
      h.release();
      await settle(el);
      expect(win.pageIndex).toBe(1);
      expect(win.items[0].id).toBe('g-01174');
      expect(win.updatesAvailable).toBe(true);
      expect(pager(el).showChip).toBe(true);
    });

    it('count-only: a change to an agent not in the store while the fit request is in flight raises the chip', async () => {
      const { h } = heldFake(2002);
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      handleUpdate('agent.g-00005.status', { agentId: 'g-00005', phase: 'stopped' });
      await flushLive(el);
      h.release();
      await settle(el);
      const win = internals(el).agentWindow;
      expect(win.memberIndex.countOnly).toBe(true);
      expect(text(el)).toContain('2,002 agents · 2,002 running, as of last refresh');
      expect(pager(el).showChip).toBe(true);
    });

    it('count-only: approximate counts and pager total show as lower bounds (ptone/scion#3426)', async () => {
      const { fake } = heldFake(2002);
      fake.approximate = true;
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.memberIndex.countOnly).toBe(true);
      expect(win.totalApproximate).toBe(true);
      expect(text(el)).toContain('2,002+ agents · 2,002+ running, as of last refresh');
      await pager(el).updateComplete;
      expect(pager(el).shadowRoot?.textContent).toContain('of 2002+');
    });

    it('count-only: exact counts and pager total have no marker', async () => {
      heldFake(2002);
      const el = await mount();
      expect(internals(el).agentWindow.totalApproximate).toBe(false);
      expect(text(el)).toContain('2,002 agents · 2,002 running, as of last refresh');
      await pager(el).updateComplete;
      expect(pager(el).shadowRoot?.textContent).toContain('of 2002');
      expect(pager(el).shadowRoot?.textContent).not.toContain('of 2002+');
    });

    it('count-only: with no live change the chip stays hidden', async () => {
      const { h } = heldFake(2002);
      h.hold();
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      h.release();
      await settle(el);
      expect(internals(el).agentWindow.memberIndex.countOnly).toBe(true);
      expect(pager(el).showChip).toBe(false);
    });
  });
});

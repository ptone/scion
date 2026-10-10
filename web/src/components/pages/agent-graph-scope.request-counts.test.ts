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
 * Exact request counts per interaction, and the fit probe.
 *
 * One part of the "/agents/graph scope and loading" suite. The suite is split
 * across agent-graph-scope.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/agent-graph-scope.ts.
 */

// @vitest-environment happy-dom

import { describe, expect, it, vi } from 'vitest';
import { stateManager } from '../../client/state.js';
import { fakeFetch, holdable, makeAgent } from './__fixtures__/global-agents-endpoint.js';
import {
  g,
  PROBE,
  ALL_PAGE,
  PROJECT_PAGE,
  SilentEventSource,
  newFake,
  settle,
  mountUnsettled,
  mountGraph,
  pick,
  liveUpdate,
  reconnect,
  ids,
  projectIds,
  banner,
  bannerText,
  holdInState,
  graphRequests,
  expectOnlyAgentRequests,
  rawUrl,
  failingFetch,
  legacyProbeFetch,
  STALE,
  listenerBalance,
  useGraphScopeHooks,
} from './__fixtures__/agent-graph-scope.js';

describe('/agents/graph scope and loading', { timeout: 60_000 }, () => {
  useGraphScopeHooks();

  describe('request counts', () => {
    const allPages = (n: number): string[] =>
      Array.from({ length: Math.min(Math.ceil(Math.max(n, 1) / 500), 4) }, (_, i) =>
        ALL_PAGE(i * 500 || undefined)
      );

    for (const n of [25, 500, 1200]) {
      it(`graph entry with the flag held, any project, issues no request (A = ${n})`, async () => {
        const fake = newFake(n);
        vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
        (await mountGraph()).remove();
        expect(stateManager.isAgentSetComplete('compact')).toBe(true);
        const before = fake.requests.length;
        for (const search of ['', '?project=p-1', '?project=p-2']) {
          (await mountGraph(search)).remove();
        }
        expect(fake.requests.length).toBe(before);
        expectOnlyAgentRequests(fake);
      });
    }

    for (const n of [25, 500, 1200, 2001]) {
      it(`unscoped graph entry, empty store (A = ${n})`, async () => {
        const fake = newFake(n);
        vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
        await mountGraph();
        expect(graphRequests(fake)).toEqual(allPages(n));
        expectOnlyAgentRequests(fake);
      });

      it(`scoped graph entry, empty store (A = ${n})`, async () => {
        const fake = newFake(n);
        vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
        await mountGraph('?project=p-1');
        expect(graphRequests(fake)).toEqual(n <= 500 ? [PROBE] : [PROBE, PROJECT_PAGE('p-1')]);
        expectOnlyAgentRequests(fake);
      });

      it(`graph entry with a non-empty store and no flag loads as from empty (A = ${n})`, async () => {
        const fake = newFake(n);
        vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
        holdInState(fake.agents.slice(0, 5), false);
        (await mountGraph()).remove();
        expect(graphRequests(fake)).toEqual(allPages(n));
        expectOnlyAgentRequests(fake);
        stateManager.setScope({ type: 'brokers-list' });
        holdInState(fake.agents.slice(0, 5), false);
        fake.requests.length = 0;
        vi.mocked(globalThis.fetch).mockClear();
        await mountGraph('?project=p-1');
        expect(graphRequests(fake)).toEqual(n <= 500 ? [PROBE] : [PROBE, PROJECT_PAGE('p-1')]);
        expectOnlyAgentRequests(fake);
      });

      it(`a resync after a graph load issues no request (A = ${n})`, async () => {
        const fake = newFake(n);
        vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
        const el = await mountGraph('?project=p-1');
        const before = fake.requests.length;
        reconnect();
        await el.updateComplete;
        await settle(el);
        expect(fake.requests.length).toBe(before);
        expect(bannerText(el, 'stale')).toBe(STALE);
        expectOnlyAgentRequests(fake);
      });
    }

    for (const n of [25, 500]) {
      it(`picker changes after a complete first load issue no request (A = ${n})`, async () => {
        const fake = newFake(n);
        vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
        const el = await mountGraph('?project=p-1');
        expect(fake.requests).toEqual([PROBE]);
        for (const p of ['', 'p-1', 'p-2', '']) await pick(el, p);
        expect(fake.requests).toHaveLength(1);
        expectOnlyAgentRequests(fake);
      });
    }

    it('picker changes at 1,200: X to Y drains Y, Y to all drains once, then none', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      await pick(el, 'p-2');
      expect(graphRequests(fake, 2)).toEqual([PROJECT_PAGE('p-2')]);
      await pick(el, '');
      expect(graphRequests(fake, 3)).toEqual(allPages(1200));
      for (const p of ['p-1', 'p-2', '']) await pick(el, p);
      expect(fake.requests).toHaveLength(6);
      expectOnlyAgentRequests(fake);
    });

    it('picker changes above 2,000: to all drains once (capped), a project drains it, back to all issues none', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(fake.requests).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      await pick(el, '');
      expect(graphRequests(fake, 2)).toEqual(allPages(2001));
      await pick(el, 'p-1');
      expect(graphRequests(fake, 6)).toEqual([PROJECT_PAGE('p-1')]);
      await pick(el, '');
      expect(fake.requests).toHaveLength(7);
      expectOnlyAgentRequests(fake);
    });
  });

  describe('fit probe', () => {
    it('a probe answered with a 500 falls back to the project drain, with no error', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, (u) => u.searchParams.has('fit'))));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(g(el).error).toBeNull();
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('a probe that fails on the network falls back to the project drain, with no error', async () => {
      const fake = newFake(25);
      const inner = fakeFetch(fake);
      vi.spyOn(console, 'warn').mockImplementation(() => {});
      vi.stubGlobal(
        'fetch',
        vi.fn((input: string | URL | Request, init?: RequestInit) => {
          const raw = rawUrl(input);
          if (new URL(raw, 'http://x').searchParams.has('fit')) {
            fake.requests.push(raw);
            return Promise.reject(new TypeError('Failed to fetch'));
          }
          return inner(input, init);
        })
      );
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(g(el).error).toBeNull();
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('after a legacy probe answer, a picker change drains the new project with no second probe', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(legacyProbeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      await pick(el, 'p-2');
      await pick(el, 'p-3');
      expect(graphRequests(fake)).toEqual([
        PROBE,
        PROJECT_PAGE('p-1'),
        PROJECT_PAGE('p-2'),
        PROJECT_PAGE('p-3'),
      ]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-3'));
    });

    it('after a probe answered with a 500, a picker change drains the new project with no second probe', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, (u) => u.searchParams.has('fit'))));
      const el = await mountGraph('?project=p-1');
      await pick(el, 'p-2');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1'), PROJECT_PAGE('p-2')]);
    });

    it('a probe answered after the first-connect timeout shows the stale banner', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      const fake = newFake(25);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('fit'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      vi.advanceTimersByTime(3000);
      expect(g(el).stale).toBe(true);
      h.release();
      await vi.waitFor(() => expect(g(el).loading).toBe(false));
      await el.updateComplete;
      expect(fake.requests).toEqual([PROBE]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(bannerText(el, 'stale')).toBe(STALE);
    });

    it('a probe sent after a late first connect has come up shows no stale banner', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      stateManager.setScope({ type: 'agent-detail', agentId: 'x' });
      const fake = newFake(25);
      const h = holdable(fakeFetch(fake), (u) => !u.searchParams.has('sort'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled('', { connectTimeoutMs: 60_000 });
      vi.advanceTimersByTime(3000);
      expect((el as unknown as { firstConnectLate: boolean }).firstConnectLate).toBe(true);
      // The unscoped drain is still waiting for the connection, so no banner yet.
      expect(g(el).stale).toBe(false);
      vi.useRealTimers();
      stateManager.sseClientInstance.dispatchEvent(new CustomEvent('connected'));
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      g(el).setProjectFilter('p-1');
      await settle(el);
      // The aborted unscoped drain page never reached the server.
      expect(h.sent.map((r) => r.url)).toEqual([ALL_PAGE()]);
      expect(h.sent[0].signal?.aborted).toBe(true);
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(g(el).stale).toBe(false);
      expect(banner(el, 'stale')).toBeNull();
    });

    it('live changes during the probe are kept, a resync during it shows the stale banner, and its epoch closes', async () => {
      const fake = newFake(25);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('fit'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const add = vi.spyOn(stateManager, 'addEventListener');
      const remove = vi.spyOn(stateManager, 'removeEventListener');
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      // Wait for the live connection, so the reconnect below is a resync.
      await stateManager.sseConnected(stateManager.scopeGeneration);
      const statusId = fake.agents[0].id;
      const victim = fake.agents[1].id;
      liveUpdate(`agent.${statusId}.status`, { agentId: statusId, phase: 'stopped' });
      liveUpdate(`agent.${victim}.deleted`, { agentId: victim });
      liveUpdate('agent.n-p1.created', makeAgent(9100, { id: 'n-p1', projectId: 'p-1' }));
      reconnect();
      h.release();
      await settle(el);
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(stateManager.getAgent(statusId)?.phase).toBe('stopped');
      expect(g(el).agents.find((a) => a.id === statusId)?.phase).toBe('stopped');
      expect(g(el).agents.some((a) => a.id === victim)).toBe(false);
      expect(stateManager.getAgent(victim)).toBeUndefined();
      expect(g(el).visibleAgents.some((a) => a.id === 'n-p1')).toBe(true);
      expect(bannerText(el, 'stale')).toBe(STALE);
      const openEpochs = (stateManager as unknown as { seedEpochs: Map<unknown, unknown> })
        .seedEpochs.size;
      expect(openEpochs).toBe(0);
      el.remove();
      for (const name of ['agent-created', 'agents-changed', 'agents-resync']) {
        expect(listenerBalance(add, remove, name)).toBe(0);
      }
    });

    it('a probe answered with a 500 still closes its seed epoch', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, (u) => u.searchParams.has('fit'))));
      const add = vi.spyOn(stateManager, 'addEventListener');
      const remove = vi.spyOn(stateManager, 'removeEventListener');
      const el = await mountGraph('?project=p-1');
      expect(
        (stateManager as unknown as { seedEpochs: Map<unknown, unknown> }).seedEpochs.size
      ).toBe(0);
      el.remove();
      for (const name of ['agent-created', 'agents-changed', 'agents-resync']) {
        expect(listenerBalance(add, remove, name)).toBe(0);
      }
    });
  });
});

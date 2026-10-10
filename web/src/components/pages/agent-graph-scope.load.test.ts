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
 * Load path, drain, picker, stale banner and state consumers.
 *
 * One part of the "/agents/graph scope and loading" suite. The suite is split
 * across agent-graph-scope.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/agent-graph-scope.ts.
 */

// @vitest-environment happy-dom

import { describe, expect, it, vi } from 'vitest';
import type { Agent } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import {
  fakeFetch,
  holdable,
  jsonResponse,
  makeAgent,
  SCOPE_CAPS,
} from './__fixtures__/global-agents-endpoint.js';
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
  mountPage,
  pick,
  liveUpdate,
  reconnect,
  ids,
  projectIds,
  banner,
  bannerText,
  clickBanner,
  treeNode,
  holdInState,
  graphRequests,
  legacyProbeFetch,
  CAPPED_ALL,
  STALE,
  AGENTS_PAGE_LOAD,
  setScopeSpy,
  useGraphScopeHooks,
} from './__fixtures__/agent-graph-scope.js';

describe('/agents/graph scope and loading', { timeout: 60_000 }, () => {
  useGraphScopeHooks();

  describe('load path', () => {
    it('a held complete set renders with no request, and picker changes issue none', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents, true);
      const el = await mountGraph('?project=p-1');
      expect(fake.requests).toEqual([]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      for (const p of ['', 'p-1', 'p-2']) await pick(el, p);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-2'));
      expect(fake.requests).toEqual([]);
    });

    it('the page only ever sets the dashboard scope, through loads and picker changes', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      setScopeSpy.mockClear();
      const el = await mountGraph('?project=p-1');
      for (const p of ['p-2', '', 'p-3']) await pick(el, p);
      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(setScopeSpy.mock.calls.length).toBeGreaterThan(0);
      for (const [scope] of setScopeSpy.mock.calls) expect(scope).toEqual({ type: 'dashboard' });
    });

    it('an unscoped graph with an empty store drains in one compact request and marks the set compact', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(graphRequests(fake)).toEqual([ALL_PAGE()]);
      expect(ids(g(el).visibleAgents)).toEqual(ids(fake.agents));
      expect(stateManager.isAgentSetComplete('compact')).toBe(true);
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
    });

    it('a scoped graph at 25 sends one complete probe, sets the flag, and picker changes issue none', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(stateManager.isAgentSetComplete('compact')).toBe(true);
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      expect(stateManager.getAgents()).toHaveLength(25);
      for (const p of ['', 'p-1', 'p-3']) await pick(el, p);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-3'));
      expect(fake.requests).toHaveLength(1);
    });

    it('a scoped graph at 1,200 probes, then drains only its project; X to all drains once and all to X issues none', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);

      // A live create in another project updates the store but not the graph.
      // The server holds both live creates from now on.
      const yNew = makeAgent(9001, { id: 'y-new', projectId: 'p-2' });
      const xNew = makeAgent(9002, { id: 'x-new', projectId: 'p-1' });
      fake.agents.unshift(xNew, yNew);
      liveUpdate('agent.y-new.created', yNew);
      await el.updateComplete;
      expect(stateManager.getAgent('y-new')).toBeDefined();
      expect(g(el).agents.some((a) => a.id === 'y-new')).toBe(false);
      // One in the scoped project joins it.
      liveUpdate('agent.x-new.created', xNew);
      await el.updateComplete;
      expect(g(el).visibleAgents.some((a) => a.id === 'x-new')).toBe(true);

      const before = fake.requests.length;
      await pick(el, '');
      expect(graphRequests(fake, before)).toEqual([ALL_PAGE(), ALL_PAGE(500), ALL_PAGE(1000)]);
      expect(stateManager.isAgentSetComplete('compact')).toBe(true);
      await pick(el, 'p-1');
      expect(fake.requests).toHaveLength(before + 3);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(projectIds(fake, 'p-1')).toContain('x-new');
    });

    it('a probe without complete: true (an older server answering a legacy page) is not complete', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(legacyProbeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('a non-empty store without the flag is not reused: the graph loads (unscoped and scoped)', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents.slice(0, 3), false);
      const el = await mountGraph();
      expect(graphRequests(fake)).toEqual([ALL_PAGE()]);
      expect(g(el).visibleAgents).toHaveLength(25);
      el.remove();

      stateManager.setScope({ type: 'brokers-list' });
      holdInState(fake.agents.slice(0, 3), false);
      fake.requests.length = 0;
      const scoped = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(ids(g(scoped).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('a live delete after the load removes the agent from the graph', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      const victim = fake.agents[4].id;
      liveUpdate(`agent.${victim}.deleted`, { agentId: victim });
      await el.updateComplete;
      expect(g(el).agents.some((a) => a.id === victim)).toBe(false);
      expect(g(el).agents).toHaveLength(24);
    });

    it('an agent deleted and then created again live stays out, as in a drain', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      const victim = fake.agents[4];
      liveUpdate(`agent.${victim.id}.deleted`, { agentId: victim.id });
      liveUpdate(`agent.${victim.id}.created`, { ...victim, phase: 'stopped' });
      await el.updateComplete;
      expect(stateManager.getDeletedAgentIds().has(victim.id)).toBe(true);
      expect(g(el).agents.some((a) => a.id === victim.id)).toBe(false);
    });

    it('a live status change updates the member object', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      const id = fake.agents[2].id;
      liveUpdate(`agent.${id}.status`, { agentId: id, phase: 'stopped' });
      await el.updateComplete;
      expect(g(el).agents.find((a) => a.id === id)?.phase).toBe('stopped');
    });
  });

  describe('drain', () => {
    it('1,201 agents over 3 pages give every node, with lineage across a page boundary', async () => {
      const fake = newFake(1201);
      const parent = fake.agents[10];
      fake.agents[1100] = { ...fake.agents[1100], ancestry: ['user-1', parent.id] } as Agent;
      parent.ancestry = ['user-1'];
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(graphRequests(fake)).toEqual([ALL_PAGE(), ALL_PAGE(500), ALL_PAGE(1000)]);
      expect(g(el).visibleAgents).toHaveLength(1201);
      expect(stateManager.isAgentSetComplete('compact')).toBe(true);
      expect(banner(el, 'incomplete')).toBeNull();
      await vi.waitFor(() => expect(treeNode(el, fake.agents[1100].id)).not.toBeNull());
      expect(treeNode(el, fake.agents[1100].id)?.querySelector('.ancestor-missing')).toBeNull();
    });

    it('2,001 agents stop after 4 requests with the capped banner, and missing ancestors are marked; Retry drains again', async () => {
      const fake = newFake(2001);
      fake.agents[5] = { ...fake.agents[5], ancestry: ['user-1', fake.agents[2000].id] } as Agent;
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(graphRequests(fake)).toEqual([
        ALL_PAGE(),
        ALL_PAGE(500),
        ALL_PAGE(1000),
        ALL_PAGE(1500),
      ]);
      expect(g(el).visibleAgents).toHaveLength(2000);
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
      expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
      await vi.waitFor(() => expect(treeNode(el, fake.agents[5].id)).not.toBeNull());
      const marker = treeNode(el, fake.agents[5].id)?.querySelector('.ancestor-missing');
      expect(marker?.getAttribute('aria-label')).toBe('Ancestor not loaded');

      await clickBanner(el, 'incomplete');
      expect(graphRequests(fake, 4)).toEqual([
        ALL_PAGE(),
        ALL_PAGE(500),
        ALL_PAGE(1000),
        ALL_PAGE(1500),
      ]);
      expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
    });

    it('a page-2 failure shows the incomplete banner with what loaded', async () => {
      const fake = newFake(1201);
      const inner = fakeFetch(fake);
      vi.stubGlobal(
        'fetch',
        vi.fn((input: string | URL | Request, init?: RequestInit) => {
          const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : '';
          if (new URL(raw, 'http://x').searchParams.get('cursor') === '500') {
            fake.requests.push(raw);
            return Promise.resolve(jsonResponse({ error: { message: 'boom' } }, 500));
          }
          return inner(input, init);
        })
      );
      const el = await mountGraph();
      expect(g(el).visibleAgents).toHaveLength(500);
      expect(bannerText(el, 'incomplete')).toBe('Incomplete: loaded 500');
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
    });

    it('a failed re-drain keeps the previous complete graph, with a banner counting the rows shown', async () => {
      const fake = newFake(1201);
      const inner = fakeFetch(fake);
      let failPage2 = false;
      vi.stubGlobal(
        'fetch',
        vi.fn((input: string | URL | Request, init?: RequestInit) => {
          const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : '';
          if (failPage2 && new URL(raw, 'http://x').searchParams.get('cursor') === '500') {
            fake.requests.push(raw);
            return Promise.resolve(jsonResponse({ error: { message: 'boom' } }, 500));
          }
          return inner(input, init);
        })
      );
      const el = await mountGraph();
      expect(g(el).visibleAgents).toHaveLength(1201);
      failPage2 = true;
      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(g(el).visibleAgents).toHaveLength(1201);
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 1,201 · showing the previous graph'
      );
    });

    it('a first-page failure with nothing loaded shows the error; Retry loads', async () => {
      const fake = newFake(25);
      fake.failAll = true;
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(g(el).error).toBe('HTTP 500');
      fake.failAll = false;
      const retry = Array.from(el.shadowRoot?.querySelectorAll('sl-button') ?? []).find((b) =>
        b.textContent?.includes('Retry')
      ) as HTMLElement;
      retry.click();
      await settle(el);
      expect(g(el).error).toBeNull();
      expect(g(el).visibleAgents).toHaveLength(25);
    });

    it('a create and a delete during an unscoped drain are kept', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.get('cursor') === '500');
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const victim = fake.agents[3].id;
      liveUpdate('agent.n-new.created', makeAgent(9003, { id: 'n-new', projectId: 'p-5' }));
      liveUpdate(`agent.${victim}.deleted`, { agentId: victim });
      h.release();
      await settle(el);
      const shown = ids(g(el).visibleAgents);
      expect(shown).toContain('n-new');
      expect(shown).not.toContain(victim);
      expect(shown).toHaveLength(1200);
      expect(stateManager.getAgent(victim)).toBeUndefined();
    });

    it('a create during a project drain joins only when it belongs to the project; a delete leaves', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('projectId'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const victim = projectIds(fake, 'p-1')[2];
      liveUpdate('agent.in-x.created', makeAgent(9004, { id: 'in-x', projectId: 'p-1' }));
      liveUpdate('agent.in-y.created', makeAgent(9005, { id: 'in-y', projectId: 'p-2' }));
      liveUpdate(`agent.${victim}.deleted`, { agentId: victim });
      h.release();
      await settle(el);
      expect(g(el).agents.some((a) => a.id === 'in-x')).toBe(true);
      expect(g(el).agents.some((a) => a.id === 'in-y')).toBe(false);
      expect(stateManager.getAgent('in-y')).toBeDefined();
      expect(g(el).agents.some((a) => a.id === victim)).toBe(false);
      expect(stateManager.getAgent(victim)).toBeUndefined();
    });

    it('a picker change aborts the in-flight drain and keeps focus and orientation', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.get('cursor') === '500');
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled('?focus=g-00003&dir=horizontal');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      g(el).setProjectFilter('p-1');
      expect(h.sent[0].signal?.aborted).toBe(true);
      await settle(el);
      // The aborted page never reached the server; the probe and the
      // project drain follow.
      expect(graphRequests(fake)).toEqual([ALL_PAGE(), PROBE, PROJECT_PAGE('p-1')]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(g(el).focusId).toBe('g-00003');
      expect(g(el).orientation).toBe('horizontal');
      expect(
        el.shadowRoot?.querySelector('scion-agent-tree-view')?.getAttribute('orientation')
      ).toBe('horizontal');
    });
  });

  describe('picker', () => {
    it('a project drained after a capped unscoped set still offers every known project', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      const all = (g(el).projects as Array<{ id: string }>).map((p) => p.id);
      expect(all).toHaveLength(7);
      const optionValues = () =>
        [...(el.shadowRoot?.querySelectorAll('sl-select sl-option') ?? [])].map((o) =>
          o.getAttribute('value')
        );
      expect(optionValues()).toEqual(all);
      await pick(el, 'p-1');
      expect(g(el).memberScope).toBe('p-1');
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect((g(el).projects as Array<{ id: string }>).map((p) => p.id)).toEqual(all);
      expect(optionValues()).toEqual(all);
    });

    it('a capped unscoped drain is reused for all projects; a project choice drains that project', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(fake.requests).toHaveLength(4);
      await pick(el, 'p-1');
      expect(graphRequests(fake, 4)).toEqual([PROJECT_PAGE('p-1')]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(banner(el, 'incomplete')).toBeNull();
      await pick(el, '');
      expect(fake.requests).toHaveLength(5);
      expect(g(el).visibleAgents).toHaveLength(2000);
      expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
    });

    it('a picker-change drain starts with no connection wait and shows no stale banner', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      // A connection wait would last a minute.
      const el = await mountGraph('?project=p-1', { connectTimeoutMs: 60_000 });
      const before = fake.requests.length;
      g(el).setProjectFilter('p-2');
      await vi.waitFor(() => expect(fake.requests.length).toBe(before + 1), { timeout: 5000 });
      expect(fake.requests[before]).toBe(PROJECT_PAGE('p-2'));
      await settle(el);
      expect(g(el).stale).toBe(false);
      expect(banner(el, 'stale')).toBeNull();
    });
  });

  describe('stale banner', () => {
    it('on the held path a resync shows the banner with no request; Refresh drains again', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents, true);
      const el = await mountGraph('?project=p-1');
      await new Promise((resolve) => setTimeout(resolve, 0));
      reconnect();
      await el.updateComplete;
      expect(bannerText(el, 'stale')).toBe(STALE);
      expect(fake.requests).toEqual([]);
      await clickBanner(el, 'stale');
      expect(graphRequests(fake)).toEqual([ALL_PAGE()]);
      expect(banner(el, 'stale')).toBeNull();
    });

    it('after a drain a resync shows the banner with no request; Refresh drains the project again', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(fake.requests).toHaveLength(2);
      reconnect();
      await el.updateComplete;
      expect(bannerText(el, 'stale')).toBe(STALE);
      expect(fake.requests).toHaveLength(2);
      await clickBanner(el, 'stale');
      expect(graphRequests(fake, 2)).toEqual([PROJECT_PAGE('p-1')]);
      expect(banner(el, 'stale')).toBeNull();
    });

    it('a drain whose live connection comes up late shows the banner', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      stateManager.setScope({ type: 'agent-detail', agentId: 'x' });
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('', { connectTimeoutMs: 5 });
      expect(fake.requests).toEqual([ALL_PAGE()]);
      expect(bannerText(el, 'stale')).toBe(STALE);
    });

    it('on the held path a late first connect shows the banner with no request', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents, true);
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled();
      expect(banner(el, 'stale')).toBeNull();
      vi.advanceTimersByTime(3000);
      await el.updateComplete;
      expect(bannerText(el, 'stale')).toBe(STALE);
      expect(fake.requests).toEqual([]);
    });
  });

  describe('state consumers after the graph', () => {
    it('a scoped graph at 25 then home issues no agents request', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      (await mountGraph('?project=p-1')).remove();
      const before = fake.requests.length;
      await mountPage('scion-page-home');
      expect(fake.requests.length - before).toBe(0);
    });

    it('a scoped graph at 1,200 then home issues one home load', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      (await mountGraph('?project=p-1')).remove();
      const before = fake.requests.length;
      await mountPage('scion-page-home');
      expect(fake.requests.slice(before)).toEqual([
        '/api/v1/agents?sort=updated&dir=desc&limit=1&fit=500&stats=1',
      ]);
      expect(fake.otherRequests).toEqual(['/api/v1/projects', '/api/v1/admin/invites/stats']);
    });

    it('a graph-drained (compact) flag satisfies home; the agents page, with its capabilities held, still loads', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      (await mountGraph()).remove();
      expect(stateManager.isAgentSetComplete('compact')).toBe(true);
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      let before = fake.requests.length;
      (await mountPage('scion-page-home')).remove();
      expect(fake.requests.length - before).toBe(0);
      // Only the flag's view can decide the agents page's reuse now.
      stateManager.seedScopeCapabilities('agent', SCOPE_CAPS);
      localStorage.setItem('scion-view-agents', 'list');
      before = fake.requests.length;
      await mountPage('scion-page-agents');
      expect(fake.requests.slice(before)).toEqual([AGENTS_PAGE_LOAD]);
    });

    it('a full flag with the capabilities held lets the agents page reuse the set with no request', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      (await mountGraph()).remove();
      stateManager.markAgentSetComplete('full');
      stateManager.seedScopeCapabilities('agent', SCOPE_CAPS);
      localStorage.setItem('scion-view-agents', 'list');
      const before = fake.requests.length;
      const el = await mountPage('scion-page-agents');
      expect(fake.requests.length - before).toBe(0);
      expect((el as unknown as { agents: Agent[] }).agents).toHaveLength(25);
    });

    it('a 0-row graph then home runs the normal empty-state load', async () => {
      const fake = newFake(0);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      (await mountGraph()).remove();
      expect(fake.requests).toEqual([ALL_PAGE()]);
      expect(stateManager.isAgentSetComplete('compact')).toBe(true);
      await mountPage('scion-page-home');
      expect(fake.requests.slice(1)).toEqual([
        '/api/v1/agents?sort=updated&dir=desc&limit=1&fit=500&stats=1',
      ]);
      expect(fake.otherRequests).toEqual(['/api/v1/projects', '/api/v1/admin/invites/stats']);
    });
  });
});

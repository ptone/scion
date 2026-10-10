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
 * Live changes during a drain, and the kept capped set.
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
  pick,
  liveUpdate,
  reconnect,
  ids,
  banner,
  bannerText,
  clickBanner,
  graphRequests,
  expectOnlyAgentRequests,
  rawUrl,
  failingFetch,
  CAPPED_ALL,
  STALE,
  treeView,
  useGraphScopeHooks,
} from './__fixtures__/agent-graph-scope.js';

describe('/agents/graph scope and loading', { timeout: 60_000 }, () => {
  useGraphScopeHooks();

  describe('live changes during a drain', () => {
    it('a resync during an unscoped drain shows the stale banner with no extra request; a status during it is kept', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.get('cursor') === '500');
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const id = fake.agents[7].id;
      liveUpdate(`agent.${id}.status`, { agentId: id, phase: 'stopped' });
      reconnect();
      h.release();
      await settle(el);
      expect(graphRequests(fake)).toEqual([ALL_PAGE(), ALL_PAGE(500), ALL_PAGE(1000)]);
      expect(g(el).agents.find((a) => a.id === id)?.phase).toBe('stopped');
      expect(bannerText(el, 'stale')).toBe(STALE);
      expectOnlyAgentRequests(fake);
    });

    it('a resync during a project drain shows the stale banner with no extra request', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('projectId'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      reconnect();
      h.release();
      await settle(el);
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(bannerText(el, 'stale')).toBe(STALE);
    });
  });

  describe('kept capped set', () => {
    it('keeps its stale state: a resync, a project, then all again shows the stale banner with no request', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      reconnect();
      await el.updateComplete;
      expect(bannerText(el, 'stale')).toBe(STALE);
      await pick(el, 'p-1');
      expect(banner(el, 'stale')).toBeNull();
      await pick(el, '');
      expect(graphRequests(fake, 4)).toEqual([PROJECT_PAGE('p-1')]);
      expect(g(el).visibleAgents).toHaveLength(2000);
      expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
      expect(bannerText(el, 'stale')).toBe(STALE);
    });

    it('a capped drain whose live connection came up late keeps that stale state', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      stateManager.setScope({ type: 'agent-detail', agentId: 'x' });
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('', { connectTimeoutMs: 5 });
      expect(fake.requests).toHaveLength(4);
      expect(g(el).cappedAll?.stale).toBe(true);
      expect(bannerText(el, 'stale')).toBe(STALE);
    });

    it('receives live creates and deletes while a project is shown', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      await pick(el, 'p-1');
      const victim = fake.agents.find((a) => a.projectId === 'p-2')!.id;
      liveUpdate('agent.n-p3.created', makeAgent(9200, { id: 'n-p3', projectId: 'p-3' }));
      liveUpdate(`agent.${victim}.deleted`, { agentId: victim });
      await el.updateComplete;
      await pick(el, '');
      expect(fake.requests).toHaveLength(5);
      const shown = ids(g(el).visibleAgents);
      expect(shown).toContain('n-p3');
      expect(shown).not.toContain(victim);
      expect(shown).toHaveLength(2000);
      expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
    });

    it('an agent beyond the cap that changes live never joins it, even once loaded by a project drain', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      const beyond = fake.agents[2000];
      expect(g(el).agents.some((a) => a.id === beyond.id)).toBe(false);
      await pick(el, beyond.projectId!);
      expect(g(el).agents.some((a) => a.id === beyond.id)).toBe(true);
      await pick(el, '');
      liveUpdate(`agent.${beyond.id}.status`, { agentId: beyond.id, phase: 'stopped' });
      await el.updateComplete;
      expect(stateManager.getAgent(beyond.id)?.phase).toBe('stopped');
      expect(g(el).agents.some((a) => a.id === beyond.id)).toBe(false);
      expect(g(el).agents).toHaveLength(2000);
    });

    it('picking all while a project drain is in flight aborts it and reuses the capped set', async () => {
      const fake = newFake(2001);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('projectId'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      const el = await mountGraph();
      h.hold(1);
      g(el).setProjectFilter('p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      g(el).setProjectFilter('');
      expect(h.sent[0].signal?.aborted).toBe(true);
      await settle(el);
      expect(fake.requests).toHaveLength(4);
      expect(h.sent).toHaveLength(1);
      expect(g(el).visibleAgents).toHaveLength(2000);
      expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
    });

    for (const [where, fails, retried] of [
      [
        'the first page',
        (u: URL) => !u.searchParams.has('cursor'),
        [ALL_PAGE(), ALL_PAGE(), ALL_PAGE()],
      ],
      [
        'page 3',
        (u: URL) => u.searchParams.get('cursor') === '1000',
        [ALL_PAGE(), ALL_PAGE(500), ALL_PAGE(1000), ALL_PAGE(1000), ALL_PAGE(1000)],
      ],
    ] as const) {
      it(`a Retry failing on ${where} keeps the same capped set and its banner`, async () => {
        const fake = newFake(2001);
        let fail = false;
        vi.stubGlobal('fetch', vi.fn(failingFetch(fake, (u) => fail && fails(u))));
        const el = await mountGraph();
        const shown = ids(g(el).visibleAgents);
        fail = true;
        await clickBanner(el, 'incomplete');
        fail = false;
        expect(graphRequests(fake, 4)).toEqual(retried);
        expect(g(el).error).toBeNull();
        expect(ids(g(el).visibleAgents)).toEqual(shown);
        expect(ids([...g(el).cappedAll!.members.values()])).toEqual(shown);
        expect(bannerText(el, 'incomplete')).toBe(`${CAPPED_ALL} · showing the previous graph`);
        expect(treeView(el)?.markMissingAncestors).toBe(true);
        // Back to all through a project: the same set again.
        await pick(el, 'p-1');
        await pick(el, '');
        expect(ids(g(el).visibleAgents)).toEqual(shown);
        expect(bannerText(el, 'incomplete')).toBe(CAPPED_ALL);
      });
    }

    it('a capped project drain shows the capped banner without the project-filter hint', async () => {
      const fake = newFake(2001);
      for (const a of fake.agents) a.projectId = 'p-1';
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([
        PROBE,
        PROJECT_PAGE('p-1'),
        PROJECT_PAGE('p-1', 500),
        PROJECT_PAGE('p-1', 1000),
        PROJECT_PAGE('p-1', 1500),
      ]);
      expect(bannerText(el, 'incomplete')).toBe(
        'Graph incomplete: 2,000 loaded (newest 2,000 checked), more exist'
      );
    });

    it('the capped banner counts the rows loaded when fewer than those checked are readable', async () => {
      const fake = newFake(2001);
      const inner = fakeFetch(fake);
      // The server drops every agent whose number is a multiple of 3 (not readable).
      vi.stubGlobal(
        'fetch',
        vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
          const resp = await inner(input, init);
          if (new URL(rawUrl(input), 'http://x').pathname !== '/api/v1/agents') return resp;
          const body = (await resp.json()) as { agents: Agent[] };
          body.agents = body.agents.filter((a) => Number(a.id.slice(2)) % 3 !== 0);
          return jsonResponse(body);
        })
      );
      const el = await mountGraph();
      expect(fake.requests).toHaveLength(4);
      expect(g(el).visibleAgents).toHaveLength(1333);
      expect(bannerText(el, 'incomplete')).toBe(
        'Graph incomplete: 1,333 loaded (newest 2,000 checked), more exist · narrow with a project filter'
      );
    });
  });
});

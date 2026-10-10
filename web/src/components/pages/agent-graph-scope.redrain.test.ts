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
 * Failed re-drains, Retry/Refresh, ancestor marker, detach/re-attach and deletes.
 *
 * One part of the "/agents/graph scope and loading" suite. The suite is split
 * across agent-graph-scope.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/agent-graph-scope.ts.
 */

// @vitest-environment happy-dom

import { describe, expect, it, vi } from 'vitest';
import type { Agent, DeletionInfo } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import {
  fakeFetch,
  holdable,
  makeAgent,
  type Fake,
} from './__fixtures__/global-agents-endpoint.js';
import {
  type TestEl,
  g,
  PROBE,
  ALL_PAGE,
  PROJECT_PAGE,
  SilentEventSource,
  newFake,
  fetchState,
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
  clickBanner,
  treeNode,
  holdInState,
  graphRequests,
  failingFetch,
  CAPPED_ALL,
  STALE,
  treeView,
  listenerBalance,
  deferredConnects,
  useGraphScopeHooks,
} from './__fixtures__/agent-graph-scope.js';

describe('/agents/graph scope and loading', { timeout: 60_000 }, () => {
  useGraphScopeHooks();

  describe('failed re-drains', () => {
    it('a partial graph whose Retry fails again keeps its own banner and rows', async () => {
      const fake = newFake(1201);
      let failing = '500';
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => u.searchParams.get('cursor') === failing))
      );
      const el = await mountGraph();
      expect(bannerText(el, 'incomplete')).toBe('Incomplete: loaded 500');
      const shown = ids(g(el).visibleAgents);
      const before = fake.requests.length;
      // This time page 2 answers and page 3 fails: more rows arrive, but the
      // drain still failed.
      failing = '1000';
      await clickBanner(el, 'incomplete');
      expect(graphRequests(fake, before)).toEqual([
        ALL_PAGE(),
        ALL_PAGE(500),
        ALL_PAGE(1000),
        ALL_PAGE(1000),
        ALL_PAGE(1000),
      ]);
      expect(ids(g(el).visibleAgents)).toEqual(shown);
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 500 · showing the previous graph'
      );
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
    });

    it('a complete graph whose Refresh fails on the first page keeps it, never saying loaded 0', async () => {
      const fake = newFake(1201);
      let fail = false;
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, () => fail)));
      const el = await mountGraph();
      reconnect();
      await el.updateComplete;
      fail = true;
      await clickBanner(el, 'stale');
      expect(g(el).error).toBeNull();
      expect(g(el).visibleAgents).toHaveLength(1201);
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 1,201 · showing the previous graph'
      );
    });

    it('a picker change after a failed Refresh of a held set clears the kept-previous banner', async () => {
      const fake = newFake(25);
      let fail = false;
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, () => fail)));
      holdInState(fake.agents, true);
      const el = await mountGraph();
      await new Promise((resolve) => setTimeout(resolve, 0));
      reconnect();
      await el.updateComplete;
      fail = true;
      await clickBanner(el, 'stale');
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 25 · showing the previous graph'
      );
      const before = fake.requests.length;
      await pick(el, 'p-1');
      expect(fake.requests).toHaveLength(before);
      expect(banner(el, 'incomplete')).toBeNull();
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('after an error view, a failed first page for the earlier project shows the error, not a previous graph', async () => {
      const fake = newFake(1200);
      const failing = new Set(['p-2']);
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => failing.has(u.searchParams.get('projectId') ?? '')))
      );
      const el = await mountGraph('?project=p-1');
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      await pick(el, 'p-2');
      expect(g(el).error).toBe('HTTP 500');
      expect(g(el).agents).toEqual([]);
      expect(g(el).memberScope).toBeNull();
      failing.add('p-1');
      await pick(el, 'p-1');
      expect(g(el).error).toBe('HTTP 500');
      expect(g(el).agents).toEqual([]);
      expect(banner(el, 'incomplete')).toBeNull();
      expect(treeView(el)).toBeNull();
    });

    it('a first-page failure for another project shows the error, not the previous graph', async () => {
      const fake = newFake(1200);
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => u.searchParams.get('projectId') === 'p-2'))
      );
      const el = await mountGraph('?project=p-1');
      await pick(el, 'p-2');
      expect(graphRequests(fake, 2)).toEqual([
        PROJECT_PAGE('p-2'),
        PROJECT_PAGE('p-2'),
        PROJECT_PAGE('p-2'),
      ]);
      expect(g(el).error).toBe('HTTP 500');
      expect(banner(el, 'incomplete')).toBeNull();
      expect(treeView(el)).toBeNull();
    });

    it('a failed multi-page unscoped drain counts as large: a project choice drains with no probe', async () => {
      const fake = newFake(1201);
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => u.searchParams.get('cursor') === '1000'))
      );
      const el = await mountGraph();
      expect(bannerText(el, 'incomplete')).toBe('Incomplete: loaded 1,000');
      const before = fake.requests.length;
      await pick(el, 'p-1');
      expect(graphRequests(fake, before)).toEqual([PROJECT_PAGE('p-1')]);
    });
  });

  describe('Retry and Refresh', () => {
    it('keep the graph shown with a loading button, and a second click while reloading sends nothing', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('projectId'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      const el = await mountGraph('?project=p-1');
      reconnect();
      await el.updateComplete;
      h.hold(1);
      (banner(el, 'stale')?.querySelector('sl-button') as HTMLElement).click();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      await el.updateComplete;
      expect(g(el).loading).toBe(false);
      expect(g(el).reloading).toBe(true);
      expect(treeView(el)).not.toBeNull();
      expect(el.shadowRoot?.querySelector('sl-spinner')).toBeNull();
      const button = banner(el, 'stale')?.querySelector('sl-button');
      expect(button?.hasAttribute('loading')).toBe(true);
      expect(button?.hasAttribute('disabled')).toBe(true);
      g(el).onReload();
      expect(h.sent).toHaveLength(2);
      expect(h.sent[1].signal?.aborted).toBe(false);
      h.release();
      await settle(el);
      expect(graphRequests(fake, 2)).toEqual([PROJECT_PAGE('p-1')]);
      expect(banner(el, 'stale')).toBeNull();
    });

    it('a picker change during a Refresh of a held set clears the reloading state and leaves Refresh enabled', async () => {
      const fake = newFake(25);
      const h = holdable(fakeFetch(fake), () => true);
      vi.stubGlobal('fetch', vi.fn(h.fn));
      holdInState(fake.agents, true);
      const el = await mountGraph();
      await new Promise((resolve) => setTimeout(resolve, 0));
      reconnect();
      await el.updateComplete;
      h.hold(1);
      (banner(el, 'stale')?.querySelector('sl-button') as HTMLElement).click();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      expect(g(el).reloading).toBe(true);
      g(el).setProjectFilter('p-2');
      await el.updateComplete;
      expect(h.sent[0].signal?.aborted).toBe(true);
      expect(g(el).reloading).toBe(false);
      await settle(el);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-2'));
      const button = banner(el, 'stale')?.querySelector('sl-button');
      expect(button?.hasAttribute('disabled')).toBe(false);
      expect(button?.hasAttribute('loading')).toBe(false);
      // A later Refresh still drains.
      await clickBanner(el, 'stale');
      expect(h.sent.map((r) => r.url)).toEqual([ALL_PAGE(), ALL_PAGE()]);
      expect(graphRequests(fake)).toEqual([ALL_PAGE()]);
      expect(banner(el, 'stale')).toBeNull();
    });

    it('the Retry button of the incomplete banner shows loading and is disabled while it drains', async () => {
      const fake = newFake(2001);
      const h = holdable(fakeFetch(fake), (u) => !u.searchParams.has('cursor'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      const el = await mountGraph();
      h.hold(1);
      (banner(el, 'incomplete')?.querySelector('sl-button') as HTMLElement).click();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      await el.updateComplete;
      expect(banner(el, 'incomplete')?.querySelector('sl-button')?.hasAttribute('loading')).toBe(
        true
      );
      expect(banner(el, 'incomplete')?.querySelector('sl-button')?.hasAttribute('disabled')).toBe(
        true
      );
      expect(treeView(el)).not.toBeNull();
      h.release();
      await settle(el);
      expect(banner(el, 'incomplete')?.querySelector('sl-button')?.hasAttribute('loading')).toBe(
        false
      );
      expect(banner(el, 'incomplete')?.querySelector('sl-button')?.hasAttribute('disabled')).toBe(
        false
      );
    });
  });

  describe('ancestor-not-loaded marker', () => {
    it('is off for a complete graph, even for an agent whose parent is gone', async () => {
      const fake = newFake(25);
      fake.agents[3] = { ...fake.agents[3], ancestry: ['user-1', 'deleted-parent'] } as Agent;
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(treeView(el)?.markMissingAncestors).toBe(false);
      await vi.waitFor(() => expect(treeNode(el, fake.agents[3].id)).not.toBeNull());
      expect(treeNode(el, fake.agents[3].id)?.querySelector('.ancestor-missing')).toBeNull();
    });

    it('is off for a complete project graph whose agent has a parent in another project', async () => {
      const fake = newFake(1200);
      const child = fake.agents.find((a) => a.projectId === 'p-1')!;
      const parent = fake.agents.find((a) => a.projectId === 'p-2')!;
      child.ancestry = ['user-1', parent.id];
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(treeView(el)?.markMissingAncestors).toBe(false);
      await vi.waitFor(() => expect(treeNode(el, child.id)).not.toBeNull());
      expect(treeNode(el, child.id)?.querySelector('.ancestor-missing')).toBeNull();
    });

    it('is on while the graph is capped or failed, and off again once it is complete', async () => {
      const fake = newFake(2001);
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => u.searchParams.get('cursor') === '500'))
      );
      const el = await mountGraph();
      expect(bannerText(el, 'incomplete')).toBe('Incomplete: loaded 500');
      expect(treeView(el)?.markMissingAncestors).toBe(true);
      el.remove();

      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el2 = await mountGraph();
      expect(bannerText(el2, 'incomplete')).toBe(CAPPED_ALL);
      expect(treeView(el2)?.markMissingAncestors).toBe(true);
      await pick(el2, 'p-1');
      expect(banner(el2, 'incomplete')).toBeNull();
      expect(treeView(el2)?.markMissingAncestors).toBe(false);
    });

    it('stays off on a complete project graph whose Refresh failed, for a parent the project filter hides', async () => {
      const fake = newFake(25);
      const child = fake.agents.find((a) => a.projectId === 'p-1')!;
      const parent = fake.agents.find((a) => a.projectId === 'p-2')!;
      child.ancestry = ['user-1', parent.id];
      let fail = false;
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, () => fail)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      await vi.waitFor(() => expect(treeNode(el, child.id)).not.toBeNull());
      expect(treeNode(el, child.id)?.querySelector('.ancestor-missing')).toBeNull();
      reconnect();
      await el.updateComplete;
      fail = true;
      await clickBanner(el, 'stale');
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 25 · showing the previous graph'
      );
      expect(g(el).agents.some((a) => a.id === parent.id)).toBe(true);
      expect(treeView(el)?.markMissingAncestors).toBe(false);
      await vi.waitFor(() => expect(treeNode(el, child.id)).not.toBeNull());
      expect(treeNode(el, child.id)?.querySelector('.ancestor-missing')).toBeNull();
    });

    it('stays off on a complete unscoped graph whose Refresh failed, for an agent whose parent is gone', async () => {
      const fake = newFake(25);
      fake.agents[3] = { ...fake.agents[3], ancestry: ['user-1', 'deleted-parent'] } as Agent;
      let fail = false;
      vi.stubGlobal('fetch', vi.fn(failingFetch(fake, () => fail)));
      const el = await mountGraph();
      reconnect();
      await el.updateComplete;
      fail = true;
      await clickBanner(el, 'stale');
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 25 · showing the previous graph'
      );
      // A second failure keeps it marked complete too.
      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 25 · showing the previous graph'
      );
      expect(treeView(el)?.markMissingAncestors).toBe(false);
      await vi.waitFor(() => expect(treeNode(el, fake.agents[3].id)).not.toBeNull());
      expect(treeNode(el, fake.agents[3].id)?.querySelector('.ancestor-missing')).toBeNull();
    });

    it('stays on for a partial or capped graph whose Retry failed', async () => {
      const fake = newFake(1201);
      fake.agents[3] = { ...fake.agents[3], ancestry: ['user-1', 'deleted-parent'] } as Agent;
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => u.searchParams.get('cursor') === '500'))
      );
      const el = await mountGraph();
      expect(bannerText(el, 'incomplete')).toBe('Incomplete: loaded 500');
      await clickBanner(el, 'incomplete');
      expect(bannerText(el, 'incomplete')).toBe(
        'Incomplete: loaded 500 · showing the previous graph'
      );
      expect(treeView(el)?.markMissingAncestors).toBe(true);
      await vi.waitFor(() => expect(treeNode(el, fake.agents[3].id)).not.toBeNull());
      expect(treeNode(el, fake.agents[3].id)?.querySelector('.ancestor-missing')).not.toBeNull();
      el.remove();

      const capped = newFake(2001);
      let fail = false;
      vi.stubGlobal('fetch', vi.fn(failingFetch(capped, () => fail)));
      const el2 = await mountGraph();
      fail = true;
      await clickBanner(el2, 'incomplete');
      expect(bannerText(el2, 'incomplete')).toBe(`${CAPPED_ALL} · showing the previous graph`);
      expect(treeView(el2)?.markMissingAncestors).toBe(true);
      // The kept capped set, reused for all projects, still marks.
      fail = false;
      await pick(el2, 'p-1');
      expect(treeView(el2)?.markMissingAncestors).toBe(false);
      await pick(el2, '');
      expect(bannerText(el2, 'incomplete')).toBe(CAPPED_ALL);
      expect(treeView(el2)?.markMissingAncestors).toBe(true);
    });
  });

  describe('detach and re-attach', () => {
    it('a detached page aborts its drain, drops its listeners and ignores live changes', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.get('cursor') === '500');
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const add = vi.spyOn(stateManager, 'addEventListener');
      const remove = vi.spyOn(stateManager, 'removeEventListener');
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      el.remove();
      expect(h.sent[0].signal?.aborted).toBe(true);
      await vi.waitFor(() => expect(fetchState().pending).toBe(0));
      await new Promise((resolve) => setTimeout(resolve, 0));
      expect(fake.requests).toEqual([ALL_PAGE()]);
      for (const name of ['agent-created', 'agents-changed', 'agents-resync']) {
        expect(listenerBalance(add, remove, name)).toBe(0);
      }
      liveUpdate('agent.n-late.created', makeAgent(9300, { id: 'n-late', projectId: 'p-1' }));
      expect(g(el).agents).toEqual([]);
    });

    it('a re-attached page starts clean: it probes again and shows no stale banner or capped set', async () => {
      const fake = newFake(1200);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      reconnect();
      await el.updateComplete;
      expect(bannerText(el, 'stale')).toBe(STALE);
      el.remove();
      expect(g(el).probed).toBe(false);
      expect(g(el).memberScope).toBeNull();
      expect(g(el).stale).toBe(false);
      expect(g(el).agents).toEqual([]);
      document.body.appendChild(el);
      await el.updateComplete;
      await settle(el);
      expect(graphRequests(fake, 2)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(banner(el, 'stale')).toBeNull();
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('a re-attached page drops a capped set it kept, and drains again', async () => {
      const fake = newFake(2001);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(g(el).cappedAll).not.toBeNull();
      el.remove();
      expect(g(el).cappedAll).toBeNull();
      // Missed while detached.
      const victim = fake.agents[0].id;
      liveUpdate(`agent.${victim}.deleted`, { agentId: victim });
      fake.agents.shift();
      document.body.appendChild(el);
      await el.updateComplete;
      await settle(el);
      expect(graphRequests(fake, 4)).toEqual([
        ALL_PAGE(),
        ALL_PAGE(500),
        ALL_PAGE(1000),
        ALL_PAGE(1500),
      ]);
      expect(g(el).agents.some((a) => a.id === victim)).toBe(false);
    });

    it('a re-attached page ignores the first-connect timer of its earlier attachment', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents, true);
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled();
      vi.advanceTimersByTime(1000);
      el.remove();
      document.body.appendChild(el);
      await el.updateComplete;
      // The earlier attachment's 3 s would end here.
      vi.advanceTimersByTime(2000);
      await el.updateComplete;
      expect(g(el).stale).toBe(false);
      stateManager.sseClientInstance.dispatchEvent(new CustomEvent('connected'));
      await stateManager.sseConnected(stateManager.scopeGeneration);
      vi.advanceTimersByTime(3000);
      await el.updateComplete;
      expect(g(el).stale).toBe(false);
      expect(banner(el, 'stale')).toBeNull();
      expect(fake.requests).toEqual([]);
    });

    it('a page re-attached after a failed multi-page drain forgets the large set and probes again', async () => {
      const fake = newFake(1200);
      vi.stubGlobal(
        'fetch',
        vi.fn(failingFetch(fake, (u) => u.searchParams.get('cursor') === '1000'))
      );
      const el = await mountGraph();
      expect(g(el).knownLarge).toBe(true);
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);
      el.remove();
      expect(g(el).knownLarge).toBe(false);
      window.history.replaceState({}, '', '/agents/graph?project=p-1');
      const before = fake.requests.length;
      document.body.appendChild(el);
      await el.updateComplete;
      await settle(el);
      expect(graphRequests(fake, before)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
    });

    it('a re-attached page does not carry a late first connect of its earlier attachment into its probe', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      stateManager.setScope({ type: 'agent-detail', agentId: 'x' });
      const fake = newFake(25);
      const h = holdable(fakeFetch(fake), (u) => !u.searchParams.has('sort'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled('', { connectTimeoutMs: 60_000 });
      vi.advanceTimersByTime(3000);
      expect((el as unknown as { firstConnectLate: boolean }).firstConnectLate).toBe(true);
      vi.useRealTimers();
      el.remove();
      window.history.replaceState({}, '', '/agents/graph?project=p-1');
      const hp = holdable(fakeFetch(fake), (u) => u.searchParams.has('sort'));
      vi.stubGlobal('fetch', vi.fn(hp.fn));
      hp.hold(1);
      document.body.appendChild(el);
      await el.updateComplete;
      await vi.waitFor(() => expect(hp.heldCount).toBe(1));
      // The connection comes up after re-attach, within the new attachment's
      // connect wait, while its probe is still in flight.
      stateManager.sseClientInstance.dispatchEvent(new CustomEvent('connected'));
      await stateManager.sseConnected(stateManager.scopeGeneration);
      hp.release();
      await settle(el);
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(g(el).stale).toBe(false);
      expect(banner(el, 'stale')).toBeNull();
    });

    it('a re-attached probe stays unconnected when its earlier attachment connects late, and its answer after the connect timeout shows the stale banner', async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      const connects = deferredConnects();
      const fake = newFake(25);
      const h1 = holdable(fakeFetch(fake), (u) => u.searchParams.has('sort'));
      vi.stubGlobal('fetch', vi.fn(h1.fn));
      h1.hold(1);
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h1.heldCount).toBe(1));
      el.remove();
      const h2 = holdable(fakeFetch(fake), (u) => u.searchParams.has('sort'));
      vi.stubGlobal('fetch', vi.fn(h2.fn));
      h2.hold(1);
      document.body.appendChild(el);
      await el.updateComplete;
      await vi.waitFor(() => expect(h2.heldCount).toBe(1));
      expect(connects).toHaveLength(2);
      // The first attachment's wait resolves while the second is still unconnected.
      connects[0].resolve();
      await connects[0].promise;
      expect((el as unknown as { firstConnected: boolean }).firstConnected).toBe(false);
      vi.advanceTimersByTime(3000);
      expect((el as unknown as { firstConnectLate: boolean }).firstConnectLate).toBe(true);
      expect((el as unknown as { firstConnected: boolean }).firstConnected).toBe(false);
      vi.useRealTimers();
      h2.release();
      await settle(el);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(g(el).stale).toBe(true);
      expect(bannerText(el, 'stale')).toBe(STALE);
    });

    it("an earlier attachment's late connect does not cancel the re-attached page's connect timer", async () => {
      vi.stubGlobal('EventSource', SilentEventSource);
      const connects = deferredConnects();
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents, true);
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
      const el = await mountUnsettled();
      vi.advanceTimersByTime(1000);
      el.remove();
      document.body.appendChild(el);
      await el.updateComplete;
      expect(connects).toHaveLength(2);
      connects[0].resolve();
      await connects[0].promise;
      vi.advanceTimersByTime(3000);
      await el.updateComplete;
      expect(g(el).stale).toBe(true);
      expect(bannerText(el, 'stale')).toBe(STALE);
      expect(fake.requests).toEqual([]);
    });
  });

  /** Sets the deletion view the fake server lists on the row for `id`. */
  function setServerDeletion(fake: Fake, id: string, deletion: DeletionInfo | null): void {
    fake.agents = fake.agents.map((a) => (a.id === id ? { ...a, deletion } : a));
  }

  describe('an agent whose delete the hub accepted', () => {
    const deletingView = (): DeletionInfo => ({
      state: 'deleting',
      soft: false,
      claim: 1,
      startedAt: new Date().toISOString(),
      leaseExpiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
    });

    function member(el: TestEl, id: string): Agent | undefined {
      return g(el).agents.find((a) => a.id === id);
    }

    it('stays in the graph with its deletion view through a re-drain of compact rows; the live delete removes it and a later drain keeps it out', async () => {
      // The hub keeps listing an agent whose delete it accepted, with the
      // deleting view, until the delete finishes; every other row is null.
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(graphRequests(fake)).toEqual([ALL_PAGE()]);
      const id = fake.agents[4].id;

      expect(stateManager.applyDeleteAccepted(id, deletingView())).toBe(true);
      setServerDeletion(fake, id, deletingView());
      (stateManager as unknown as { flush(): void }).flush();
      await el.updateComplete;
      expect(member(el, id)?.deletion?.state).toBe('deleting');
      expect(g(el).agents).toHaveLength(25);
      expect(treeNode(el, id)).not.toBeNull();

      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(graphRequests(fake, 1)).toEqual([ALL_PAGE()]);
      expect(g(el).agents).toHaveLength(25);
      expect(member(el, id)?.deletion?.state).toBe('deleting');
      // The re-drained row carries the deleting view; every other row stays clear.
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(fake.agents[5].id)?.deletion).toBeNull();

      liveUpdate(`agent.${id}.deleted`, { agentId: id });
      await el.updateComplete;
      expect(member(el, id)).toBeUndefined();
      expect(g(el).agents).toHaveLength(24);
      expect(treeNode(el, id)).toBeNull();

      // The server still lists the row: a drain after the delete leaves it out.
      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(graphRequests(fake, 2)).toEqual([ALL_PAGE()]);
      expect(member(el, id)).toBeUndefined();
      expect(g(el).agents).toHaveLength(24);
      expect(stateManager.getAgent(id)).toBeUndefined();
    });

    it('a deletion view that arrives live during an unscoped drain is kept on the row; the live delete then removes it', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.get('cursor') === '500');
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const id = fake.agents[3].id;
      liveUpdate(`agent.${id}.status`, { agentId: id, deletion: deletingView() });
      h.release();
      await settle(el);
      expect(g(el).agents).toHaveLength(1200);
      expect(member(el, id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');

      liveUpdate(`agent.${id}.deleted`, { agentId: id });
      await el.updateComplete;
      expect(member(el, id)).toBeUndefined();
      expect(g(el).agents).toHaveLength(1199);
    });

    it('a deletion view that arrives live during a project drain is kept on the row', async () => {
      const fake = newFake(1200);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('projectId'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const id = projectIds(fake, 'p-1')[2];
      liveUpdate(`agent.${id}.status`, { agentId: id, deletion: deletingView() });
      h.release();
      await settle(el);
      expect(ids(g(el).agents)).toEqual(projectIds(fake, 'p-1'));
      expect(member(el, id)?.deletion?.state).toBe('deleting');
    });

    it('a deletion view that arrives live during the fit probe is kept on the row; the live delete then removes it', async () => {
      const fake = newFake(25);
      const h = holdable(fakeFetch(fake), (u) => u.searchParams.has('fit'));
      vi.stubGlobal('fetch', vi.fn(h.fn));
      h.hold(1);
      const el = await mountUnsettled('?project=p-1');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const id = projectIds(fake, 'p-1')[0];
      liveUpdate(`agent.${id}.status`, { agentId: id, deletion: deletingView() });
      h.release();
      await settle(el);
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      expect(member(el, id)?.deletion?.state).toBe('deleting');

      liveUpdate(`agent.${id}.deleted`, { agentId: id });
      await el.updateComplete;
      expect(member(el, id)).toBeUndefined();
      expect(g(el).visibleAgents.some((a) => a.id === id)).toBe(false);
    });

    it('a deletion view and full fields the store held before the fit probe survive its compact seed', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const [id, fullId] = projectIds(fake, 'p-1');
      // An earlier page loaded full objects (with their applied config); the set is not marked complete.
      holdInState(
        fake.agents.map((a) =>
          a.id === fullId ? ({ ...a, appliedConfig: { image: 'img:full' } } as Agent) : a
        ),
        false
      );
      expect(stateManager.applyDeleteAccepted(id, deletingView())).toBe(true);
      // After the 202 the server lists the row with the deleting view.
      setServerDeletion(fake, id, deletingView());
      (stateManager as unknown as { flush(): void }).flush();
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(member(el, id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(fullId)?.deletion).toBeNull();
      expect(member(el, fullId)?.appliedConfig?.image).toBe('img:full');
      expect(stateManager.getAgent(fullId)?.appliedConfig?.image).toBe('img:full');
    });

    it("an older hub's compact row without the deletion key keeps the store's view through the fit probe", async () => {
      const fake = newFake(25);
      for (const a of fake.agents) delete (a as { deletion?: unknown }).deletion;
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const [id, otherId] = projectIds(fake, 'p-1');
      holdInState(fake.agents, false);
      expect(stateManager.applyDeleteAccepted(id, deletingView())).toBe(true);
      (stateManager as unknown as { flush(): void }).flush();
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect('deletion' in fake.agents[0]).toBe(false);
      expect(member(el, id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(otherId)?.deletion).toBeUndefined();
    });

    it('a held complete set shows the row with its deletion view, with no request', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      holdInState(fake.agents, true);
      const id = fake.agents[6].id;
      expect(stateManager.applyDeleteAccepted(id, deletingView())).toBe(true);
      (stateManager as unknown as { flush(): void }).flush();
      const el = await mountGraph();
      expect(fake.requests).toEqual([]);
      expect(g(el).agents).toHaveLength(25);
      expect(member(el, id)?.deletion?.state).toBe('deleting');
    });
  });

  describe('a cold-loaded compact set with deletion views', () => {
    const deletingView = (): DeletionInfo => ({
      state: 'deleting',
      soft: false,
      claim: 2,
      startedAt: new Date().toISOString(),
      leaseExpiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
    });
    const failedView = (): DeletionInfo => ({
      state: 'failed',
      code: 'runtime_error',
      error: 'broker unreachable',
      soft: false,
      claim: 1,
      startedAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
    });

    /**
     * Gives `failedId` a failed view and `deletingId` a deleting one, and
     * every other row an explicit null, as the hub's compact rows carry.
     */
    function withDeletions(fake: Fake, failedId: string, deletingId: string): void {
      fake.agents = fake.agents.map((a) => ({
        ...a,
        deletion: a.id === failedId ? failedView() : a.id === deletingId ? deletingView() : null,
      }));
    }

    /** The compact deletion badge's text and title on the graph node for `id`. */
    async function nodeBadge(
      el: TestEl,
      id: string
    ): Promise<{ text: string; title: string } | null> {
      const badge = treeNode(el, id)?.querySelector<
        HTMLElement & { updateComplete: Promise<boolean> }
      >('scion-deletion-badge');
      await badge?.updateComplete;
      const inner = badge?.shadowRoot?.querySelector<HTMLElement>('.badge');
      return inner ? { text: inner.textContent?.trim() ?? '', title: inner.title } : null;
    }

    async function expectBadges(el: TestEl, failedId: string, deletingId: string, plainId: string) {
      expect(await nodeBadge(el, failedId)).toEqual({
        text: 'Delete failed',
        title: 'Delete failed: broker unreachable',
      });
      expect(await nodeBadge(el, deletingId)).toEqual({ text: 'Deleting…', title: 'Deleting…' });
      expect(await nodeBadge(el, plainId)).toBeNull();
    }

    it('the unscoped drain renders a failed and a deleting row with the compact badge at once', async () => {
      const fake = newFake(25);
      const [failedId, deletingId, plainId] = [
        fake.agents[2].id,
        fake.agents[5].id,
        fake.agents[7].id,
      ];
      withDeletions(fake, failedId, deletingId);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      expect(graphRequests(fake)).toEqual([ALL_PAGE()]);
      expect(g(el).agents).toHaveLength(25);
      await expectBadges(el, failedId, deletingId, plainId);
    });

    it('the fit probe renders a failed and a deleting row with the compact badge at once', async () => {
      const fake = newFake(25);
      const [failedId, deletingId, plainId] = projectIds(fake, 'p-1');
      withDeletions(fake, failedId, deletingId);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      expect(ids(g(el).visibleAgents)).toEqual(projectIds(fake, 'p-1'));
      await expectBadges(el, failedId, deletingId, plainId);
    });

    it("the server's deletion value replaces the store's on the fit probe and on a re-drain, null included", async () => {
      const fake = newFake(25);
      const [clearedId, retriedId, plainId] = projectIds(fake, 'p-1');
      // The store holds a failed view and an older deleting view; the server
      // has since cleared the first and lists a retried delete (claim 2) for
      // the second.
      holdInState(
        fake.agents.map((a) =>
          a.id === clearedId
            ? { ...a, deletion: failedView() }
            : a.id === retriedId
              ? { ...a, deletion: { ...deletingView(), claim: 1 } }
              : a
        ),
        false
      );
      setServerDeletion(fake, retriedId, deletingView());
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE]);
      const expectServerViews = async (): Promise<void> => {
        expect(stateManager.getAgent(clearedId)?.deletion).toBeNull();
        expect(stateManager.getAgent(retriedId)?.deletion).toMatchObject({
          state: 'deleting',
          claim: 2,
        });
        expect(await nodeBadge(el, clearedId)).toBeNull();
        expect(await nodeBadge(el, retriedId)).toEqual({ text: 'Deleting…', title: 'Deleting…' });
        expect(await nodeBadge(el, plainId)).toBeNull();
      };
      await expectServerViews();

      // Live views that the server later cleared or replaced, whose clearing
      // events the dropped connection missed; the re-drain restores the
      // server's values.
      liveUpdate(`agent.${clearedId}.status`, { agentId: clearedId, deletion: failedView() });
      liveUpdate(`agent.${retriedId}.status`, {
        agentId: retriedId,
        deletion: { ...deletingView(), claim: 1 },
      });
      await el.updateComplete;
      expect(await nodeBadge(el, clearedId)).toEqual({
        text: 'Delete failed',
        title: 'Delete failed: broker unreachable',
      });
      expect(stateManager.getAgent(retriedId)?.deletion?.claim).toBe(1);
      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(graphRequests(fake, 1)).toEqual([ALL_PAGE()]);
      await expectServerViews();
    });

    it('the project drain renders a failed and a deleting row with the compact badge at once', async () => {
      const fake = newFake(1200);
      const [failedId, deletingId, plainId] = projectIds(fake, 'p-1');
      withDeletions(fake, failedId, deletingId);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph('?project=p-1');
      expect(graphRequests(fake)).toEqual([PROBE, PROJECT_PAGE('p-1')]);
      expect(ids(g(el).agents)).toEqual(projectIds(fake, 'p-1'));
      await expectBadges(el, failedId, deletingId, plainId);
    });
  });

  describe('a live delete followed by a live create of the same ID', () => {
    it('the store drops the create, and a drain whose server still lists the row keeps it out', async () => {
      const fake = newFake(25);
      vi.stubGlobal('fetch', vi.fn(fakeFetch(fake)));
      const el = await mountGraph();
      const victim = fake.agents[4];
      liveUpdate(`agent.${victim.id}.deleted`, { agentId: victim.id });
      liveUpdate(`agent.${victim.id}.created`, { ...victim, phase: 'stopped' });
      await el.updateComplete;
      expect(stateManager.getAgent(victim.id)).toBeUndefined();
      expect(g(el).agents.some((a) => a.id === victim.id)).toBe(false);

      reconnect();
      await el.updateComplete;
      await clickBanner(el, 'stale');
      expect(graphRequests(fake, 1)).toEqual([ALL_PAGE()]);
      expect(g(el).agents.some((a) => a.id === victim.id)).toBe(false);
      expect(g(el).agents).toHaveLength(24);
      expect(stateManager.getAgent(victim.id)).toBeUndefined();
    });
  });
});

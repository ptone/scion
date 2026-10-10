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
 * Capped sets, banners, errors, empty states, request counts, and deletes.
 *
 * One part of the "scion-page-agents — agent list window" suite. The suite is split
 * across agents-agent-window.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/agents-agent-window.ts.
 */

import { describe, it, expect, vi } from 'vitest';
import type { Agent, DeletionInfo } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
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
  mount,
  query,
  unmount,
  text,
  typeLabel,
  commitLabel,
  setView,
  pager,
  handleUpdate,
  flushLive,
  deferLiveFlush,
  watchDeleteFlush,
  reconnect,
  stubFake,
  hasStopAll,
  useAgentsWindowHooks,
} from './__fixtures__/agents-agent-window.js';

// Stop All asks for confirmation first; the tests confirm it.
vi.mock('../shared/confirm-dialog.js', () => ({ showConfirm: vi.fn(() => Promise.resolve(true)) }));

// Each test mounts the page and drives real fetch handling and render passes, which is slow on a
// loaded machine. The timeout is a suite option: a test's timeout is fixed when it is collected,
// so setting it from a hook would not apply.
describe('scion-page-agents — agent list window', { timeout: 30_000 }, () => {
  useAgentsWindowHooks();

  describe('a capped mine set', () => {
    it('a live create in a capped mine set raises the stale flag under the capped banner, with no request', async () => {
      // 2,001 of 4,002 agents are in the fake mine scope.
      const fake: Fake = {
        agents: Array.from({ length: 4002 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-scope-agents', 'mine');
      localStorage.setItem('scion-view-agents', 'graph');
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('capped');
      expect(fake.requests.every((r) => query(r).get('scope') === 'mine')).toBe(true);
      expect(win.stale).toBe(false);
      const n = fake.requests.length;
      handleUpdate('agent.new-3.created', { ...makeAgent(9000), id: 'new-3', agentId: 'new-3' });
      await flushLive(el);
      expect(internals(el).agents.some((a) => a.id === 'new-3')).toBe(false);
      expect(win.stale).toBe(true);
      expect(win.banner?.kind).toBe('capped');
      expect(el.shadowRoot?.querySelector('.agent-window-banner')?.textContent).toContain(
        'more exist'
      );
      expect(fake.requests.length).toBe(n);
    }, 30_000);
  });

  describe('a capped global set', () => {
    it('2,001 agents in the tree view: four requests end capped with the banner, the flag is not set, a lifecycle refresh is free and the banner Refresh drains four again', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 2001 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-view-agents', 'graph');
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(fake.requests.length).toBe(4);
      expect(fake.requests.every((u) => !query(u).has('sort'))).toBe(true);
      expect(win.state).toBe('capped');
      expect(win.incompleteReason).toBe('capped');
      expect(internals(el).agents).toHaveLength(2000);
      const banner = () => el.shadowRoot?.querySelector('.agent-window-banner');
      expect(banner()?.textContent).toContain('2,000 loaded (newest 2,000 checked), more exist');
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
      expect(stateManager.isAgentSetComplete('compact')).toBe(false);

      internals(el).backgroundRefresh('lifecycle-refresh');
      await settle(el);
      expect(fake.requests.length).toBe(4);
      expect(win.state).toBe('capped');

      (banner()?.querySelector('sl-tag') as HTMLElement).click();
      await settle(el);
      expect(fake.requests.length).toBe(8);
      expect(win.state).toBe('capped');
      expect(banner()?.textContent).toContain('2,000 loaded (newest 2,000 checked), more exist');
      expect(stateManager.isAgentSetComplete('full')).toBe(false);
    }, 30_000);
  });

  describe('the window banner above an empty filter result', () => {
    it('a capped set whose phase filter matches nothing keeps the capped banner above "No Matching Agents"', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 2001 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      // A mode filter is complete-needing: the set is a capped drain.
      localStorage.setItem('scion-filter-agents-mode', 'project');
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('capped');
      const n = fake.requests.length;
      // Every agent is running, so a stopped filter matches nothing loaded.
      internals(el).setPhaseFilter('stopped');
      await settle(el);
      expect(fake.requests.length).toBe(n);
      expect(internals(el).agentWindow.state).toBe('capped');
      expect(text(el)).toContain('No Matching Agents');
      expect(el.shadowRoot?.querySelector('.agent-window-banner')?.textContent).toContain(
        '2,000 loaded (newest 2,000 checked), more exist'
      );
    });

    it('a held set marked stale whose phase filter matches nothing keeps the stale banner', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-view-agents', 'graph');
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('held');
      reconnect();
      await settle(el);
      setView(el, 'list');
      internals(el).setPhaseFilter('stopped');
      await settle(el);
      expect(internals(el).agentWindow.state).toBe('held');
      expect(text(el)).toContain('No Matching Agents');
      expect(el.shadowRoot?.querySelector('.agent-window-banner')?.textContent).toContain(
        'may be stale'
      );
    });
  });

  describe('live membership while paged', () => {
    it('mine paged: a live create raises the chip and issues no request', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      localStorage.setItem('scion-scope-agents', 'mine');
      const el = await mount();
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(pager(el).showChip).toBe(false);
      handleUpdate('agent.new-2.created', { ...makeAgent(5002), id: 'new-2', agentId: 'new-2' });
      await flushLive(el);
      expect(pager(el).showChip).toBe(true);
      expect(fake.requests.length).toBe(1);
    });
  });

  describe('the error path', () => {
    it('a failed page load sets the error and Retry loads again', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
        failAll: true,
      };
      stubFake(fake);
      const el = await mount();
      expect(internals(el).error).toBeTruthy();
      expect(text(el)).toContain('Failed to Load Agents');
      fake.failAll = false;
      const retry = Array.from(el.shadowRoot?.querySelectorAll('sl-button') ?? []).find((b) =>
        b.textContent?.includes('Retry')
      ) as HTMLElement;
      retry.click();
      await settle(el);
      expect(internals(el).error).toBeNull();
      expect(internals(el).agentWindow.display.length).toBe(25);
    });

    it('a label 400 sets the error, as today', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
        badLabels: new Set(['bad=1']),
      };
      stubFake(fake);
      const el = await mount();
      commitLabel(el, 'bad=1');
      await settle(el);
      expect(internals(el).error).toBe('invalid label');
    });

    it('a failed lifecycle refresh keeps the current data', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      fake.failAll = true;
      vi.spyOn(console, 'warn').mockImplementation(() => {});
      internals(el).backgroundRefresh('lifecycle-refresh');
      await settle(el);
      expect(internals(el).error).toBeNull();
      expect(internals(el).agentWindow.display.length).toBe(25);
    });
  });

  describe('empty states', () => {
    const cases: Array<[scope: string, expected: string]> = [
      ['all', 'No Agents Found'],
      ['mine', "You haven't created any agents yet."],
      ['shared', 'No Shared Agents'],
    ];
    for (const [scope, expected] of cases) {
      it(`scope ${scope} with no agents shows "${expected}"`, async () => {
        stubFake({ agents: [], requests: [] });
        if (scope !== 'all') localStorage.setItem('scion-scope-agents', scope);
        const el = await mount();
        expect(text(el)).toContain(expected);
      });
    }

    it('a phase filter matching nothing shows "No Matching Agents", locally and while paged', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      internals(el).setPhaseFilter('stopped');
      await settle(el);
      expect(text(el)).toContain('No Matching Agents');
      unmount(el);

      stateManager.setScope({ type: 'brokers-list' });
      fake.agents = Array.from({ length: 1200 }, (_, i) => makeAgent(i));
      const el2 = await mount();
      expect(internals(el2).agentWindow.state).toBe('paged');
      expect(text(el2)).toContain('No Matching Agents');
    });

    for (const scope of ['mine', 'shared'] as const) {
      it(`scope ${scope}: a phase filter matching nothing shows "No Matching Agents", locally and while paged`, async () => {
        const fake: Fake = {
          agents: Array.from({ length: 25 }, (_, i) => makeAgent(i)),
          requests: [],
        };
        stubFake(fake);
        localStorage.setItem('scion-scope-agents', scope);
        const el = await mount();
        expect(internals(el).agentWindow.state).toBe('small');
        expect(internals(el).agentWindow.stats.total).toBeGreaterThan(0);
        internals(el).setPhaseFilter('stopped');
        await settle(el);
        expect(text(el)).toContain('No Matching Agents');
        expect(text(el)).not.toContain('No Shared Agents');
        expect(text(el)).not.toContain("You haven't created any agents yet.");
        unmount(el);

        stateManager.setScope({ type: 'brokers-list' });
        fake.agents = Array.from({ length: 3000 }, (_, i) => makeAgent(i));
        const el2 = await mount();
        expect(internals(el2).agentWindow.state).toBe('paged');
        expect(query(fake.requests.at(-1)!).get('scope')).toBe(scope);
        expect(query(fake.requests.at(-1)!).get('phase')).toBe('stopped');
        expect(text(el2)).toContain('No Matching Agents');
        expect(text(el2)).not.toContain('No Shared Agents');
        expect(text(el2)).not.toContain("You haven't created any agents yet.");
      }, 30_000);
    }
  });

  describe('count-only mode above 2,000 agents', () => {
    it('shows the counts as of last refresh; a delete, a create and a phase change leave them unchanged and raise the chip; the chip issues one request with stats and updates them; Stop All stays visible', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 2002 }, (_, i) =>
          makeAgent(i, { phase: i < 10 ? 'running' : 'stopped' })
        ),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(fake.requests.length).toBe(1);
      expect(win.state).toBe('paged');
      expect(win.memberIndex.countOnly).toBe(true);
      expect(text(el)).toContain('2,002 agents · 10 running, as of last refresh');
      expect(pager(el).chipText).toBe('counts may have changed · Refresh');
      expect(hasStopAll(el)).toBe(true);

      const clickChip = async () => {
        pager(el).dispatchEvent(new CustomEvent('chip-click'));
        await settle(el);
      };

      // A delete off the page.
      handleUpdate('agent.g-00005.deleted', {});
      await flushLive(el);
      expect(text(el)).toContain('2,002 agents · 10 running, as of last refresh');
      expect(pager(el).showChip).toBe(true);
      fake.agents = fake.agents.filter((a) => a.id !== 'g-00005');
      await clickChip();
      expect(fake.requests.length).toBe(2);
      expect(new URL(fake.requests[1], 'http://x').searchParams.get('stats')).toBe('1');
      expect(text(el)).toContain('2,001 agents · 9 running, as of last refresh');
      expect(pager(el).showChip).toBe(false);

      // A create.
      const created = makeAgent(9000, { phase: 'running' });
      handleUpdate(`agent.${created.id}.created`, { ...created, agentId: created.id });
      await flushLive(el);
      expect(text(el)).toContain('2,001 agents · 9 running, as of last refresh');
      expect(pager(el).showChip).toBe(true);
      fake.agents = [...fake.agents, created];
      await clickChip();
      expect(fake.requests.length).toBe(3);
      expect(text(el)).toContain('2,002 agents · 10 running, as of last refresh');

      // A phase change off the page.
      handleUpdate('agent.g-00001.status', { agentId: 'g-00001', phase: 'stopped' });
      await flushLive(el);
      expect(text(el)).toContain('2,002 agents · 10 running, as of last refresh');
      expect(pager(el).showChip).toBe(true);
      expect(hasStopAll(el)).toBe(true);
    });

    it('Stop All is hidden when the snapshot has no running agent', async () => {
      stubFake({
        agents: Array.from({ length: 2001 }, (_, i) => makeAgent(i, { phase: 'stopped' })),
        requests: [],
      });
      const el = await mount();
      expect(internals(el).agentWindow.memberIndex.countOnly).toBe(true);
      expect(hasStopAll(el)).toBe(false);
    });
  });

  describe('request counts per interaction at 25, 500 and 1,200 agents', () => {
    /**
     * A mixed fixture: every third agent stopped, alternating env=prod and
     * env=dev, and every fifth agent also carries a bare `team` key, so the
     * phase and label steps really narrow the set.
     */
    const mixedAgent = (i: number): Agent =>
      makeAgent(i, {
        phase: i % 3 === 0 ? 'stopped' : 'running',
        labels: { env: i % 2 === 0 ? 'prod' : 'dev', ...(i % 5 === 0 ? { team: 'core' } : {}) },
      });
    const byUpdated = (dir: 'asc' | 'desc') => (a: Agent, b: Agent) =>
      (dir === 'asc' ? 1 : -1) * (a.updated ?? '').localeCompare(b.updated ?? '');

    /** After a step: the window's total and the rendered rows match the fixture under this filter and dir. */
    const expectRows =
      (keep: (a: Agent) => boolean, dir: 'asc' | 'desc') =>
      (el: TestEl, fake: Fake): void => {
        const expected = fake.agents.filter(keep).sort(byUpdated(dir));
        const win = internals(el).agentWindow;
        expect(win.total).toBe(expected.length);
        const page = expected.slice(0, 25).map((a) => a.id);
        expect(win.items.map((a) => a.id)).toEqual(page);
        expect(el.shadowRoot?.querySelectorAll('tbody tr').length).toBe(page.length);
      };

    type Step = [
      label: string,
      run: (el: TestEl) => void | Promise<void>,
      check?: (el: TestEl, fake: Fake) => void,
    ];
    /** After a reconnect: the chip while paged, the stale flag in the local states (the capped banner still wins). */
    const signalsReconnect = (el: TestEl): void => {
      const win = internals(el).agentWindow;
      if (win.state === 'paged') {
        expect(win.updatesAvailable).toBe(true);
        expect(pager(el).showChip).toBe(true);
      } else {
        expect(win.stale).toBe(true);
        if (win.state !== 'capped') expect(text(el)).toContain('may be stale');
      }
    };
    const stopAll = async (el: TestEl): Promise<void> => {
      await internals(el).handleStopAll();
    };

    const steps: Step[] = [
      ['switch to grid view', (el) => setView(el, 'grid')],
      ['switch to list view', (el) => setView(el, 'list')],
      ['flip the updated sort direction', (el) => internals(el).toggleSort('updated')],
      [
        'filter phase running',
        (el) => internals(el).setPhaseFilter('running'),
        expectRows((a) => a.phase === 'running', 'asc'),
      ],
      ['clear the phase filter', (el) => internals(el).setPhaseFilter('')],
      ['next page', (el) => internals(el).agentWindow.next()],
      ['previous page', (el) => internals(el).agentWindow.prev()],
      ['type a label without committing it', (el) => typeLabel(el, 'env=pr')],
      [
        'commit label env=prod',
        (el) => commitLabel(el, 'env=prod'),
        expectRows((a) => a.labels?.env === 'prod', 'asc'),
      ],
      ['lifecycle refresh', (el) => internals(el).backgroundRefresh('lifecycle-refresh')],
      ['stop-all refresh', stopAll, expectRows((a) => a.labels?.env === 'prod', 'asc')],
      ['live connection reconnect', () => reconnect(), signalsReconnect],
      ['clear label env=prod', (el) => commitLabel(el, '')],
      [
        'commit bare-key label team',
        (el) => commitLabel(el, 'team'),
        expectRows((a) => !!a.labels && 'team' in a.labels, 'asc'),
      ],
      ['clear bare-key label team', (el) => commitLabel(el, '')],
      ['switch scope to mine', (el) => internals(el).setScope('mine')],
      ['switch scope back to all', (el) => internals(el).setScope('all')],
      ['switch to tree view', (el) => setView(el, 'graph')],
      ['switch from tree back to list view', (el) => setView(el, 'list')],
      ['filter mode branch', (el) => internals(el).setModeFilter('branch')],
      ['clear the mode filter', (el) => internals(el).setModeFilter('')],
      ['sort by name', (el) => internals(el).toggleSort('name')],
      ['sort by updated after the name sort', (el) => internals(el).toggleSort('updated')],
      ['filter phase running after the tree', (el) => internals(el).setPhaseFilter('running')],
      ['next page after the tree', (el) => internals(el).agentWindow.next()],
      [
        'lifecycle refresh after the tree',
        (el) => internals(el).backgroundRefresh('lifecycle-refresh'),
      ],
      ['stop-all refresh after the tree', stopAll],
      ['live connection reconnect after the tree', () => reconnect(), signalsReconnect],
    ];

    // A bare-key label is complete-needing: it drains (one legacy request
    // at 500 or fewer agents, three at 1,200), and clearing it sends one
    // fit request.
    const small = {
      // prettier-ignore
      costs: [0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 0, 1, 1, 1, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 1, 1, 0],
      states: steps.map(() => 'small'),
    };
    const cases: Array<{ count: number; costs: number[]; states: string[] }> = [
      { count: 25, ...small },
      { count: 500, ...small },
      {
        count: 1200,
        // prettier-ignore
        costs:  [0, 0, 1, 1, 1, 1, 1, 0, 1, 1, 1, 0, 1, 3, 1, 1, 1, 3, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0],
        // prettier-ignore
        states: ['paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged', 'paged',
          'paged', 'paged', 'paged', 'paged', 'held', 'paged', 'paged', 'paged', 'held', 'held',
          'held', 'held', 'held', 'held', 'held', 'held', 'held', 'held', 'held'],
      },
    ];

    for (const c of cases) {
      it(`${c.count} agents: one request on page load, then the documented cost per interaction`, async () => {
        const fake: Fake = {
          agents: Array.from({ length: c.count }, (_, i) => mixedAgent(i)),
          requests: [],
        };
        stubFake(fake);
        const el = await mount();
        expect(fake.requests.length).toBe(1);
        expect(internals(el).agentWindow.state).toBe(c.count > 500 ? 'paged' : 'small');

        const costs: number[] = [];
        const states: string[] = [];
        for (const [name, run, check] of steps) {
          const n = fake.requests.length;
          await run(el);
          await settle(el);
          costs.push(fake.requests.length - n);
          states.push(internals(el).agentWindow.state);
          if (check) {
            try {
              check(el, fake);
            } catch (err) {
              throw new Error(`after "${name}": ${(err as Error).message}`, { cause: err });
            }
          }
        }
        const named = (xs: Array<number | string>) => steps.map(([name], i) => `${name}: ${xs[i]}`);
        expect(named(costs)).toEqual(named(c.costs));
        expect(named(states)).toEqual(named(c.states));

        // Client navigation back to /agents: the flag is held (small or a
        // completed drain), so the reuse branch issues no request. The
        // persisted running filter still applies.
        unmount(el);
        const n = fake.requests.length;
        const el2 = await mount();
        expect(fake.requests.length - n).toBe(0);
        expect(internals(el2).agentWindow.total).toBe(
          fake.agents.filter((a) => a.phase === 'running').length
        );
      }, 60_000);
    }

    it('2,001 agents: the counts chip is one paged request with stats', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 2001 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      handleUpdate('agent.g-00003.deleted', {});
      await flushLive(el);
      expect(pager(el).showChip).toBe(true);
      const n = fake.requests.length;
      pager(el).dispatchEvent(new CustomEvent('chip-click'));
      await settle(el);
      expect(fake.requests.length - n).toBe(1);
      const q = new URL(fake.requests.at(-1)!, 'http://x').searchParams;
      expect(q.get('stats')).toBe('1');
      expect(q.get('limit')).toBe('25');
      expect(q.has('fit')).toBe(false);
    });
  });

  describe('a deleted agent on the page, and a later create for it', () => {
    async function pagedWithDeleted(restore: boolean): Promise<{
      el: TestEl;
      fake: Fake;
      id: string;
    }> {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      stubFake(fake);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const id = win.items[0].id;
      handleUpdate(`agent.${id}.deleted`, {});
      await flushLive(el);
      if (restore) {
        const restored = fake.agents.find((a) => a.id === id)!;
        handleUpdate(`agent.${id}.created`, { ...restored, agentId: id });
        await flushLive(el);
      }
      return { el, fake, id };
    }

    it('a restored agent the response still lists stays off the page and raises no chip on refresh, and repeated refreshes stay chip-free', async () => {
      const { el, fake, id } = await pagedWithDeleted(true);
      const win = internals(el).agentWindow;
      expect(stateManager.getAgent(id)).toBeUndefined();
      for (let i = 0; i < 3; i++) {
        const before = fake.requests.length;
        await win.refresh();
        await settle(el);
        expect(fake.requests.length - before).toBe(1);
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
      const { el, id } = await pagedWithDeleted(false);
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
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const id = win.items[0].id;

      h.hold();
      const refreshed = win.refresh();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      handleUpdate(`agent.${id}.deleted`, {});
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
      expect(h.sent).toHaveLength(3);
      expect(win.items.some((a) => a.id === id)).toBe(false);
      expect(win.items).toHaveLength(24);
      expect(win.updatesAvailable).toBe(false);
    });

    it('a delete applied during an in-flight refresh whose flush lands after the response raises the chip for that refresh only', async () => {
      const fake: Fake = {
        agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
        requests: [],
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const id = win.items[0].id;

      h.hold();
      const refreshed = win.refresh();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      const restoreFlush = deferLiveFlush();
      const watch = watchDeleteFlush(id, () => win.loading);
      try {
        handleUpdate(`agent.${id}.deleted`, {});
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
      expect(h.sent).toHaveLength(3);
      expect(win.items.some((a) => a.id === id)).toBe(false);
      expect(win.items).toHaveLength(24);
      expect(win.updatesAvailable).toBe(false);
    });

    for (const serverLists of [false, true]) {
      it(`a create for an agent deleted while a refresh is in flight is ignored, and the agent stays off the page (${serverLists ? 'the response still lists it' : 'the response no longer lists it'})`, async () => {
        const fake: Fake = {
          agents: Array.from({ length: 1200 }, (_, i) => makeAgent(i)),
          requests: [],
        };
        const h = holdable(fakeFetch(fake), isGlobalAgentsList);
        vi.stubGlobal('fetch', h.fn);
        const el = await mount();
        const win = internals(el).agentWindow;
        expect(win.state).toBe('paged');
        const id = win.items[0].id;
        const agent = fake.agents.find((a) => a.id === id)!;

        h.hold();
        const refreshed = win.refresh();
        await vi.waitFor(() => expect(h.heldCount).toBe(1));
        if (!serverLists) fake.agents = fake.agents.filter((a) => a.id !== id);
        handleUpdate(`agent.${id}.deleted`, {});
        await flushLive(el);
        handleUpdate(`agent.${id}.created`, { ...agent, agentId: id });
        await flushLive(el);
        expect(stateManager.getAgent(id)).toBeUndefined();
        h.release();
        await refreshed;
        await settle(el);

        expect(h.sent).toHaveLength(2);
        expect(win.items.some((a) => a.id === id)).toBe(false);
        expect(win.memberIndex.has(id)).toBe(false);
        // The page rows are the response's minus the deleted agent. A row
        // the response still lists is left out as deleted, so the page is
        // one row short: the backfill chip.
        expect(win.items).toHaveLength(serverLists ? 24 : 25);
        expect(win.updatesAvailable).toBe(serverLists);
      });
    }
  });

  describe('a delete the hub accepts with a 202', () => {
    const lifecycleCaps = { actions: ['read', 'update', 'delete', 'lifecycle', 'stop_all'] };
    const deletingView = (): DeletionInfo => ({
      state: 'deleting',
      soft: false,
      claim: 1,
      startedAt: new Date().toISOString(),
      leaseExpiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
    });
    // Every REST row carries `deletion: null`, as the hub sends it.
    const deletableAgents = (count: number): Agent[] =>
      Array.from({ length: count }, (_, i) =>
        makeAgent(i, { _capabilities: lifecycleCaps, deletion: null })
      );
    const altClick = { altKey: true } as MouseEvent;

    /** The grid card or list row that renders agent `id`, if any. */
    function rowOf(el: TestEl, id: string): Element | null {
      const link = el.shadowRoot?.querySelector(`a[href="/agents/${id}"]`);
      return link?.closest('tr, .agent-card') ?? null;
    }

    async function badgeText(row: Element | null): Promise<string> {
      const badge = row?.querySelector('scion-deletion-badge') as
        | (HTMLElement & { updateComplete: Promise<boolean> })
        | null;
      await badge?.updateComplete;
      return badge?.shadowRoot?.querySelector('.badge')?.textContent?.trim() ?? '';
    }

    /** The failure banner's title in that row (ptone/scion#2483 phase 2), if shown. */
    async function bannerText(row: Element | null): Promise<string> {
      const banner = row?.querySelector('scion-deletion-banner') as
        | (HTMLElement & { updateComplete: Promise<boolean> })
        | null;
      await banner?.updateComplete;
      return banner?.shadowRoot?.querySelector('.title')?.textContent?.trim() ?? '';
    }

    function icons(row: Element | null, name: string): number {
      return row?.querySelectorAll(`sl-icon[name="${name}"]`).length ?? 0;
    }

    /** Whether agent `id` is a row of the current window, and its stored deletion state. */
    function shownDeletion(el: TestEl, id: string): string | null | undefined {
      const row = internals(el).agentWindow.items.find((a) => a.id === id);
      return row ? (row.deletion?.state ?? null) : undefined;
    }

    interface StateRow {
      state: 'small' | 'held' | 'paged' | 'capped';
      count: number;
      /** A complete-needing mode filter makes the page drain. */
      drain: boolean;
    }
    const states: StateRow[] = [
      { state: 'small', count: 30, drain: false },
      { state: 'held', count: 600, drain: true },
      { state: 'paged', count: 1200, drain: false },
      { state: 'capped', count: 2001, drain: true },
    ];

    describe.each(
      states.flatMap((st) => (['list', 'grid'] as const).map((view) => ({ ...st, view })))
    )('$state, $view view', ({ state, count, drain, view }) => {
      it('keeps the row, shows Deleting at once, hides its actions, sends no list request and counts no dropped row; the live delete then removes it in place', async () => {
        const fake: Fake = {
          agents: deletableAgents(count),
          requests: [],
          deletion: deletingView(),
          deletes: [],
        };
        stubFake(fake);
        localStorage.setItem('scion-view-agents', view);
        if (drain) localStorage.setItem('scion-filter-agents-mode', 'project');
        const el = await mount();
        const win = internals(el).agentWindow;
        expect(win.state).toBe(state);
        const id = win.items[0].id;
        const rowsBefore = win.items.length;
        const statsBefore = { ...win.stats };
        const requestsBefore = fake.requests.length;
        expect(icons(rowOf(el, id), 'trash')).toBe(1);
        expect(icons(rowOf(el, id), 'stop-circle')).toBe(1);

        await internals(el).handleAgentAction(id, 'delete', altClick);
        await flushLive(el);

        expect(fake.deletes).toEqual([`/api/v1/agents/${id}`]);
        expect(shownDeletion(el, id)).toBe('deleting');
        expect(win.items).toHaveLength(rowsBefore);
        expect(await badgeText(rowOf(el, id))).toBe('Deleting…');
        expect(icons(rowOf(el, id), 'trash')).toBe(0);
        expect(icons(rowOf(el, id), 'stop-circle')).toBe(0);
        // Not a dropped row: no chip, unchanged counts, no refresh.
        expect(win.updatesAvailable).toBe(false);
        expect(win.stats).toEqual(statsBefore);
        await settle(el);
        expect(fake.requests.length).toBe(requestsBefore);
        // Other rows keep their actions.
        const other = win.items[1].id;
        expect(icons(rowOf(el, other), 'trash')).toBe(1);
        expect(await badgeText(rowOf(el, other))).toBe('');

        handleUpdate(`agent.${id}.deleted`, {});
        await flushLive(el);
        expect(shownDeletion(el, id)).toBeUndefined();
        expect(rowOf(el, id)).toBeNull();
        expect(stateManager.getAgent(id)).toBeUndefined();
        if (state === 'paged') {
          expect(win.memberIndex.has(id)).toBe(false);
        } else {
          expect(internals(el).agents.some((a) => a.id === id)).toBe(false);
        }
        if (state !== 'capped') expect(win.stats.total).toBe(statsBefore.total - 1);
        await settle(el);
        expect(fake.requests.length).toBe(requestsBefore);
      });
    });

    it('a force delete accepted with a 202 keeps the paged row deleting and sends no list request', async () => {
      const fake: Fake = {
        agents: deletableAgents(1200),
        requests: [],
        deletion: deletingView(),
        deletes: [],
        deleteUnreachable: true,
      };
      stubFake(fake);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const id = win.items[0].id;
      const before = fake.requests.length;

      await internals(el).handleAgentAction(id, 'delete', altClick);
      await flushLive(el);

      expect(fake.deletes).toEqual([`/api/v1/agents/${id}`, `/api/v1/agents/${id}?force=true`]);
      expect(shownDeletion(el, id)).toBe('deleting');
      expect(win.items).toHaveLength(25);
      expect(await badgeText(rowOf(el, id))).toBe('Deleting…');
      expect(icons(rowOf(el, id), 'trash')).toBe(0);
      expect(win.updatesAvailable).toBe(false);
      await settle(el);
      expect(fake.requests.length).toBe(before);
    });

    for (const { state, count } of [
      { state: 'small', count: 30 },
      { state: 'held', count: 600 },
    ] as const) {
      it(`a force delete accepted with a 202 keeps the ${state} row in place before any live flush and sends no list request`, async () => {
        const fake: Fake = {
          agents: deletableAgents(count),
          requests: [],
          deletion: deletingView(),
          deletes: [],
          deleteUnreachable: true,
        };
        stubFake(fake);
        if (state === 'held') localStorage.setItem('scion-filter-agents-mode', 'project');
        const el = await mount();
        const win = internals(el).agentWindow;
        expect(win.state).toBe(state);
        const id = win.items[2].id;
        const agentIds = internals(el).agents.map((a) => a.id);
        const itemIds = win.items.map((a) => a.id);
        const before = fake.requests.length;

        await internals(el).handleAgentAction(id, 'delete', altClick);

        // Before the store's coalesced flush: the row is neither removed
        // nor moved.
        expect(fake.deletes).toEqual([`/api/v1/agents/${id}`, `/api/v1/agents/${id}?force=true`]);
        expect(internals(el).agents.map((a) => a.id)).toEqual(agentIds);
        expect(win.items.map((a) => a.id)).toEqual(itemIds);
        expect(fake.requests.length).toBe(before);

        await flushLive(el);
        expect(internals(el).agents.map((a) => a.id)).toEqual(agentIds);
        expect(win.items.map((a) => a.id)).toEqual(itemIds);
        expect(shownDeletion(el, id)).toBe('deleting');
        expect(win.updatesAvailable).toBe(false);
        await settle(el);
        expect(fake.requests.length).toBe(before);
      });
    }

    it('a delete answered 200 still leaves the held set at once and sends the lifecycle refresh', async () => {
      const fake: Fake = { agents: deletableAgents(30), requests: [], deletes: [] };
      stubFake(fake);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('small');
      const id = win.items[0].id;
      const before = fake.requests.length;
      fake.agents = fake.agents.filter((a) => a.id !== id);
      await internals(el).handleAgentAction(id, 'delete', altClick);
      expect(internals(el).agents.some((a) => a.id === id)).toBe(false);
      await settle(el);
      expect(fake.deletes).toEqual([`/api/v1/agents/${id}`]);
      expect(fake.requests.length).toBe(before + 1);
      expect(win.items.some((a) => a.id === id)).toBe(false);
    });

    it('a chip refresh response that predates the 202 keeps the row deleting and counts no dropped row', async () => {
      const fake: Fake = {
        agents: deletableAgents(1200),
        requests: [],
        deletion: deletingView(),
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const id = win.items[0].id;
      const total = win.stats.total;

      h.hold();
      const refreshed = win.refresh();
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      await internals(el).handleAgentAction(id, 'delete', altClick);
      await flushLive(el);
      expect(shownDeletion(el, id)).toBe('deleting');
      // The held response lists the row with `deletion: null`.
      h.release();
      await refreshed;
      await settle(el);

      expect(h.sent).toHaveLength(2);
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');
      expect(shownDeletion(el, id)).toBe('deleting');
      expect(await badgeText(rowOf(el, id))).toBe('Deleting…');
      expect(icons(rowOf(el, id), 'trash')).toBe(0);
      expect(win.items).toHaveLength(25);
      expect(win.updatesAvailable).toBe(false);
      expect(win.stats.total).toBe(total);

      handleUpdate(`agent.${id}.deleted`, {});
      await flushLive(el);
      expect(shownDeletion(el, id)).toBeUndefined();
      expect(win.memberIndex.has(id)).toBe(false);
      expect(win.stats.total).toBe(total - 1);
    });

    it('a drain response that predates the 202 keeps the row deleting in the held set', async () => {
      const fake: Fake = {
        agents: deletableAgents(600),
        requests: [],
        deletion: deletingView(),
      };
      const h = holdable(fakeFetch(fake), isGlobalAgentsList);
      vi.stubGlobal('fetch', h.fn);
      localStorage.setItem('scion-filter-agents-mode', 'project');
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('held');
      const id = win.items[0].id;
      const sentBefore = h.sent.length;

      // A label commit drains the whole set again; hold its first page.
      h.hold();
      commitLabel(el, 'env=prod');
      await vi.waitFor(() => expect(h.heldCount).toBe(1));
      await internals(el).handleAgentAction(id, 'delete', altClick);
      await flushLive(el);
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');
      h.release();
      await settle(el);

      expect(h.sent.length - sentBefore).toBe(2);
      expect(win.state).toBe('held');
      expect(internals(el).agents).toHaveLength(600);
      expect(internals(el).agents.find((a) => a.id === id)?.deletion?.state).toBe('deleting');
      expect(stateManager.getAgent(id)?.deletion?.state).toBe('deleting');
      expect(await badgeText(rowOf(el, id))).toBe('Deleting…');
      expect(icons(rowOf(el, id), 'trash')).toBe(0);

      handleUpdate(`agent.${id}.deleted`, {});
      await flushLive(el);
      expect(internals(el).agents.some((a) => a.id === id)).toBe(false);
      expect(rowOf(el, id)).toBeNull();
    });

    it('paged: the lease timer watches the page rows and flips a deleting row to Delete interrupted', async () => {
      const fake: Fake = { agents: deletableAgents(1200), requests: [] };
      stubFake(fake);
      const el = await mount();
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      expect(internals(el).agents).toEqual([]);
      const id = win.items[0].id;

      // Fake clock only after mount (mount waits on real timers).
      let badge: string;
      let banner: string;
      let trash: number;
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
      try {
        handleUpdate(`agent.${id}.status`, {
          deletion: {
            state: 'deleting',
            soft: false,
            claim: 1,
            startedAt: new Date().toISOString(),
            leaseExpiresAt: new Date(Date.now() + 20_000).toISOString(),
          },
        });
        await flushLive(el);
        expect(await badgeText(rowOf(el, id))).toBe('Deleting…');

        vi.advanceTimersByTime(19_000);
        await el.updateComplete;
        expect(await badgeText(rowOf(el, id))).toBe('Deleting…');

        // Nothing else re-renders the page: only the lease timer can.
        vi.advanceTimersByTime(1_000);
        await el.updateComplete;
        badge = await badgeText(rowOf(el, id));
        banner = await bannerText(rowOf(el, id));
        trash = icons(rowOf(el, id), 'trash');
      } finally {
        vi.useRealTimers();
      }
      // Phase 2: a failed (here client-flipped) view shows the failure
      // banner; the badge is for a live delete only.
      expect(badge).toBe('');
      expect(banner).toBe('Delete interrupted');
      expect(trash).toBe(1);
    });
  });
});

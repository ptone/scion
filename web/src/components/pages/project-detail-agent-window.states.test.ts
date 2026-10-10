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
 * Scope switches, capped stats, local states, page-level requests, and deletes.
 *
 * One part of the "project-detail — agent list window" suite. The suite is split
 * across project-detail-agent-window.*.test.ts so its sections run in parallel
 * workers; shared helpers live in ./__fixtures__/project-detail-agent-window.ts.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { Agent, DeletionInfo } from '../../shared/types.js';
import { stateManager } from '../../client/state.js';
import { PROJECT_AGENTS_FIT_THRESHOLD } from '../../client/agent-list-window.js';
import { AgentDrainRunner } from '../../client/agent-drain.js';
import { runAgentDelete } from '../../client/agent-delete.js';
import { showConfirm } from '../shared/confirm-dialog.js';
import { showToast } from '../../utils/toast.js';
import { START_BLOCKED_BY_DELETE_MESSAGE } from '../../shared/agent-deletion.js';
import {
  jsonResponse,
  makeAgent,
  type AgentsRequest,
  createFetchHandler,
  type TestEl,
  requestUrl,
  stubFetch,
  createComponent,
  labelInput,
  viewToggle,
  internals,
  flushLive,
  settle,
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

  describe('a scope switch during a drain', () => {
    it('aborts the page fetch and sends no further page request', async () => {
      const projectId = 'p-scope-drain';
      localStorage.setItem('scion-view-project-agents', 'graph');
      const agents = Array.from({ length: 2001 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const inner = createRealisticFetchHandler({
        projectId,
        projectCaps: { actions: ['read'] },
        agents,
        requests,
      });
      const legacy: Array<{ url: string; signal: AbortSignal | undefined }> = [];
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        if (u.pathname === `/api/v1/projects/${projectId}/agents`) {
          legacy.push({ url: raw, signal: init?.signal ?? undefined });
          // The page is navigated away while page 2 is in flight.
          if (legacy.length === 2) stateManager.setScope({ type: 'brokers-list' });
        }
        return inner(input, init);
      });
      const el = await createComponent(projectId);
      await vi.waitFor(() => expect(internals(el).agentsLoading).toBe(false));
      await settle(el);
      expect(legacy).toHaveLength(2);
      expect(legacy[1].signal?.aborted).toBe(true);
      expect(new URL(legacy[1].url, 'http://x').searchParams.get('cursor')).toBe('500');
    });
  });

  describe('capped stats', () => {
    it('a capped set marks the Agents and Running stats as incomplete', async () => {
      const projectId = 'p-capped-stats';
      localStorage.setItem('scion-view-project-agents', 'graph');
      const agents = Array.from({ length: 2001 }, (_, i) =>
        makeAgent(i, { projectId, phase: i % 2 === 0 ? 'running' : 'stopped' })
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
      await vi.waitFor(() => expect(internals(el).agentWindow.state).toBe('capped'));
      await el.updateComplete;
      const stats = Array.from(el.shadowRoot!.querySelectorAll('.stat')).map((n) =>
        (n.textContent ?? '').replace(/\s+/g, ' ').trim()
      );
      expect(stats[0]).toBe('Agents 2,000loaded (newest 2,000 checked), more exist');
      expect(stats[1]).toBe('Running 1,000among loaded (newest 2,000 checked), more exist');
    });

    it('a complete set shows plain formatted stats', async () => {
      const projectId = 'p-plain-stats';
      localStorage.setItem('scion-view-project-agents', 'graph');
      const agents = Array.from({ length: 1200 }, (_, i) => makeAgent(i, { projectId }));
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
      await vi.waitFor(() => expect(internals(el).agentWindow.state).toBe('held'));
      await el.updateComplete;
      const stats = Array.from(el.shadowRoot!.querySelectorAll('.stat')).map((n) =>
        (n.textContent ?? '').replace(/\s+/g, ' ').trim()
      );
      expect(stats[0]).toBe('Agents 1,200');
      expect(stats[1]).toBe('Running 1,200');
      expect(el.shadowRoot!.querySelector('.stat-incomplete')).toBeNull();
    });
  });

  describe('full-view pages replace stored agents', () => {
    it('a field missing from a later full-view page is gone from the store', async () => {
      const projectId = 'p-replace';
      localStorage.setItem('scion-view-project-agents', 'list');
      let agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, taskSummary: 'old task' })
      );
      const requests: AgentsRequest[] = [];
      stubFetch((input: string | URL | Request, init?: RequestInit) =>
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        })(input, init)
      );
      const el = await createComponent(projectId);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('paged');
      const id = win.items[0].id;
      expect(stateManager.getAgent(id)?.taskSummary).toBe('old task');
      agents = agents.map((a) => {
        const { taskSummary: _dropped, ...rest } = a;
        return rest as Agent;
      });
      await win.refresh();
      expect(stateManager.getAgent(id)).toBeDefined();
      expect(stateManager.getAgent(id)?.taskSummary).toBeUndefined();
    });
  });

  describe('a refused label keeps the typed label filter on the kept rows', () => {
    for (const path of [
      { name: 'the sorted request', sort: 'updated' },
      { name: 'the drain', sort: 'name' },
    ] as const) {
      it(`a label 400 on ${path.name} keeps the previous rows filtered by the typed label`, async () => {
        const projectId = `p-label-400-preview-${path.sort}`;
        localStorage.setItem('scion-view-project-agents', 'list');
        localStorage.setItem(
          `scion-sort-project-agents-${projectId}`,
          JSON.stringify({ field: path.sort, dir: path.sort === 'name' ? 'asc' : 'desc' })
        );
        const agents = Array.from({ length: 10 }, (_, i) =>
          makeAgent(i, { projectId, labels: { env: i % 2 === 0 ? 'prod' : 'dev' } })
        );
        const requests: AgentsRequest[] = [];
        let refuse = false;
        const inner = createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        });
        stubFetch((input: string | URL | Request, init?: RequestInit) => {
          const raw =
            typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
          const u = new URL(raw, 'http://localhost');
          if (
            refuse &&
            u.pathname === `/api/v1/projects/${projectId}/agents` &&
            u.searchParams.get('label') === 'env=prod'
          ) {
            requests.push({ url: raw });
            return Promise.resolve(jsonResponse({ error: { message: 'bad label' } }, 400));
          }
          return inner(input, init);
        });
        const el = await createComponent(projectId);
        await settle(el);
        expect(internals(el).agentWindow.state).toBe('small');
        expect(internals(el).agentWindow.items).toHaveLength(10);

        refuse = true;
        const n = requests.length;
        const input = labelInput(el)!;
        input.value = 'env=prod';
        input.dispatchEvent(new Event('sl-input'));
        input.dispatchEvent(new Event('sl-change'));
        await settle(el);

        expect(requests.length - n).toBe(1); // the refused request, not retried
        expect(internals(el).committedLabel).toBe('');
        const prodIds = agents
          .filter((a) => a.labels?.env === 'prod')
          .map((a) => a.id)
          .sort();
        const shown = internals(el)
          .agentWindow.items.map((a) => a.id)
          .sort();
        expect(shown).toEqual(prodIds);
        expect(el.shadowRoot!.querySelectorAll('.agent-table-container tbody tr')).toHaveLength(5);
        expect(labelInput(el)!.value).toBe('env=prod');
      });
    }
  });

  describe('a reconnect in the local states', () => {
    const reconnect = () => {
      const sse = (stateManager as unknown as { sseClientInstance: EventTarget }).sseClientInstance;
      sse.dispatchEvent(new CustomEvent('disconnected'));
      sse.dispatchEvent(new CustomEvent('connected'));
    };
    const bannerText = (el: TestEl) =>
      el.shadowRoot!.querySelector('.agent-window-banner')?.textContent ?? '';

    for (const c of [
      { state: 'small', count: 5, view: 'list', banner: 'may be stale' },
      { state: 'held', count: 1200, view: 'graph', banner: 'may be stale' },
      // The capped banner wins over the stale one.
      { state: 'capped', count: 100, view: 'graph', banner: '80 loaded (newest 2,000 checked)' },
    ] as const) {
      it(`${c.state}: a reconnect marks the set as possibly stale with no request`, async () => {
        const projectId = `p-reconnect-${c.state}`;
        localStorage.setItem('scion-view-project-agents', c.view);
        const agents = Array.from({ length: c.count }, (_, i) => makeAgent(i, { projectId }));
        const requests: AgentsRequest[] = [];
        stubFetch(
          createRealisticFetchHandler({
            projectId,
            projectCaps: { actions: ['read'] },
            agents,
            requests,
            legacyTruncated: c.state === 'capped',
          })
        );
        const el = await createComponent(projectId);
        await settle(el);
        expect(internals(el).agentWindow.state).toBe(c.state);
        expect(internals(el).agentWindow.stale).toBe(false);
        if (c.state !== 'capped') expect(bannerText(el)).toBe('');
        const n = requests.length;

        reconnect();
        await settle(el);

        expect(internals(el).agentWindow.stale).toBe(true);
        expect(bannerText(el)).toContain(c.banner);
        expect(requests.length).toBe(n);
      });
    }
  });

  describe('drain failures and the read filter at page level', () => {
    it('a drain whose second page fails after retries shows "Incomplete: loaded N" and incomplete stats that do not claim 2,000 were checked', async () => {
      const projectId = 'p-drain-page2-fails';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'name', dir: 'asc' })
      );
      const agents = Array.from({ length: 600 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
          failLegacyCursor: '500',
        })
      );
      const el = await createComponent(projectId);
      await settle(el);
      // The first page, then the second page tried three times.
      expect(requests).toHaveLength(4);
      expect(requests.slice(1).every((r) => r.url.includes('cursor=500'))).toBe(true);
      expect(internals(el).agentWindow.state).toBe('capped');
      expect(el.shadowRoot!.querySelector('.agent-window-banner')?.textContent).toContain(
        'Incomplete: loaded 500'
      );
      const stats = Array.from(el.shadowRoot!.querySelectorAll('.stat')).map((n) =>
        (n.textContent ?? '').replace(/\s+/g, ' ').trim()
      );
      expect(stats[0]).toBe('Agents 500loaded, incomplete');
      expect(stats[1]).toMatch(/^Running \d+among loaded, incomplete$/);
      expect(el.shadowRoot!.textContent).not.toContain('newest 2,000 checked');
    });

    it('2,001 agents of which 700 of the newest 2,000 are readable: "700 loaded (newest 2,000 checked), more exist"', async () => {
      const projectId = 'p-drain-read-filtered';
      localStorage.setItem('scion-view-project-agents', 'graph');
      const agents = Array.from({ length: 2001 }, (_, i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      stubFetch(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
          readable: (a) => Number(a.id.slice(2)) % 20 < 7,
        })
      );
      const el = await createComponent(projectId);
      await settle(el);
      expect(requests).toHaveLength(4);
      expect(internals(el).agentWindow.state).toBe('capped');
      expect(internals(el).agents).toHaveLength(700);
      expect(el.shadowRoot!.querySelector('.agent-window-banner')?.textContent).toContain(
        '700 loaded (newest 2,000 checked), more exist'
      );
    });
  });

  describe('leaving the capped state', () => {
    it('a capped set returns to paged on a switch to the updated sort', async () => {
      const projectId = 'p-capped-to-paged';
      localStorage.setItem('scion-view-project-agents', 'list');
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
          legacyTruncated: true,
        })
      );
      const el = await createComponent(projectId);
      await settle(el);
      expect(internals(el).agentWindow.state).toBe('capped');
      const n = requests.length;

      internals(el).toggleSort('updated');
      await settle(el);

      expect(requests.length - n).toBe(1);
      expect(requests[requests.length - 1].url).toContain('sort=updated');
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(el.shadowRoot!.querySelector('.agent-window-banner')).toBeNull();
    });

    it('a capped set whose label was refused stays capped on a switch to the updated sort; a new label retries once', async () => {
      const projectId = 'p-capped-refused';
      localStorage.setItem('scion-view-project-agents', 'list');
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
          refuseSortedAbove: 50,
        })
      );
      const el = await createComponent(projectId);
      await settle(el);
      // The refused sorted request, then a four-page drain.
      expect(requests).toHaveLength(5);
      expect(internals(el).agentWindow.state).toBe('capped');

      let n = requests.length;
      internals(el).toggleSort('name');
      await settle(el);
      internals(el).toggleSort('updated');
      await settle(el);
      internals(el).toggleSort('updated'); // dir flip
      await settle(el);
      expect(requests.length - n).toBe(0);
      expect(internals(el).agentWindow.state).toBe('capped');

      n = requests.length;
      const input = labelInput(el)!;
      input.value = 'env=prod';
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
      await settle(el);
      expect(requests.length - n).toBe(5);
      expect(requests[n].url).toContain('sort=updated');
      expect(internals(el).agentWindow.state).toBe('capped');
    });
  });

  describe('small state: a live reorder across a local page boundary', () => {
    it('an activity bump moves an agent from page 2 to page 1 with no request and no page change', async () => {
      const projectId = 'p-small-reorder';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 30 }, (_, i) => makeAgent(i, { projectId }));
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
      await settle(el);
      const win = internals(el).agentWindow;
      expect(win.state).toBe('small');
      await win.next();
      await el.updateComplete;
      expect(win.pageIndex).toBe(1);
      // Updated desc: a-29 first; page 2 holds the five oldest.
      expect(win.items.map((a) => a.id)).toEqual(['a-4', 'a-3', 'a-2', 'a-1', 'a-0']);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({
        subject: `project.${projectId}.agent.status`,
        data: { agentId: 'a-0', phase: 'running', lastActivityEvent: '2026-03-01T00:00:00Z' },
      });
      await flushLive(el);

      expect(win.pageIndex).toBe(1);
      expect(win.items.map((a) => a.id)).toEqual(['a-5', 'a-4', 'a-3', 'a-2', 'a-1']);
      expect(win.display[0].id).toBe('a-0');
      expect(requests).toHaveLength(1);

      await win.prev();
      await el.updateComplete;
      expect(win.items[0].id).toBe('a-0');
      expect(win.items).toHaveLength(25);
      expect(requests).toHaveLength(1);
    });
  });

  describe('label typing and the debounce window', () => {
    it('label typing sends no request even after the debounce window', async () => {
      const projectId = 'p-label-debounce';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 60 }, (_, i) =>
        makeAgent(i, { projectId, labels: { env: i % 2 === 0 ? 'prod' : 'dev' } })
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
      await settle(el);
      expect(internals(el).agentWindow.state).toBe('paged');
      const n = requests.length;

      vi.useFakeTimers();
      try {
        const input = labelInput(el)!;
        for (const value of ['e', 'en', 'env', 'env=', 'env=prod']) {
          input.value = value;
          input.dispatchEvent(new Event('sl-input'));
          await vi.advanceTimersByTimeAsync(100);
        }
        await vi.advanceTimersByTimeAsync(2000);
        expect(requests.length).toBe(n);
        expect(internals(el).agentsLoading).toBe(false);
      } finally {
        vi.useRealTimers();
      }
      await el.updateComplete;
      expect(requests.length).toBe(n);
      expect(internals(el).agentWindow.items.every((a) => a.labels?.env === 'prod')).toBe(true);
    });
  });

  describe('page-level agents requests the tests hold, fail or answer', () => {
    /**
     * Mounts the page over the realistic handler. `ctl.holdNext()` holds
     * the next agents GET open until `ctl.release()`; `ctl.failAgents` and
     * `ctl.failProjectOnce` answer 500. Every agents GET is recorded with
     * its signal.
     */
    function controlledFetch(projectId: string, agents: Agent[]) {
      const requests: AgentsRequest[] = [];
      const inner = createRealisticFetchHandler({
        projectId,
        projectCaps: { actions: ['read'] },
        agents,
        requests,
      });
      const sent: Array<{ url: string; signal: AbortSignal | undefined }> = [];
      const gates: Array<() => void> = [];
      const ctl = {
        requests,
        sent,
        toHold: 0,
        failAgents: false,
        failProjectOnce: false,
        holdNext(): void {
          ctl.toHold++;
        },
        get heldCount(): number {
          return gates.length;
        },
        release(): void {
          for (const open of gates.splice(0)) open();
        },
      };
      stubFetch(async (input: string | URL | Request, init?: RequestInit) => {
        const raw =
          typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
        const u = new URL(raw, 'http://localhost');
        const signal = init?.signal ?? undefined;
        if (u.pathname === `/api/v1/projects/${projectId}` && ctl.failProjectOnce) {
          ctl.failProjectOnce = false;
          return jsonResponse({ error: { message: 'project boom' } }, 500);
        }
        if (u.pathname === `/api/v1/projects/${projectId}/agents`) {
          sent.push({ url: raw, signal });
          // The response is computed now, so it predates any live event
          // the test emits while it is held.
          const res = ctl.failAgents
            ? (requests.push({ url: raw }), jsonResponse({ error: { message: 'boom' } }, 500))
            : await inner(input, init);
          if (ctl.toHold > 0) {
            ctl.toHold--;
            await new Promise<void>((resolve, reject) => {
              gates.push(resolve);
              signal?.addEventListener(
                'abort',
                () => reject(Object.assign(new Error('aborted'), { name: 'AbortError' })),
                { once: true }
              );
            });
          }
          if (signal?.aborted) throw Object.assign(new Error('aborted'), { name: 'AbortError' });
          return res;
        }
        return inner(input, init);
      });
      return ctl;
    }

    const commitLabel = (el: TestEl, value: string) => {
      const input = labelInput(el)!;
      input.value = value;
      input.dispatchEvent(new Event('sl-input'));
      input.dispatchEvent(new Event('sl-change'));
    };

    const emitLabelledCreate = (projectId: string, id: string, env: string) =>
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
          labels: { env },
        },
      });

    for (const path of [
      { name: 'a complete fit request', view: 'list', count: 10 },
      { name: 'a drain (the tree view)', view: 'graph', count: 1200 },
    ]) {
      it(`a live create during ${path.name} under a committed k=v label joins only if it matches the label`, async () => {
        const projectId = `p-label-create-${path.view}`;
        localStorage.setItem('scion-view-project-agents', path.view);
        const agents = Array.from({ length: path.count }, (_, i) =>
          makeAgent(i, { projectId, labels: { env: i % 2 === 0 ? 'prod' : 'dev' } })
        );
        const ctl = controlledFetch(projectId, agents);
        const el = await createComponent(projectId);
        await settle(el);
        const n = ctl.sent.length;
        ctl.holdNext();
        commitLabel(el, 'env=prod');
        await vi.waitFor(() => expect(ctl.heldCount).toBe(1));
        const q = new URL(ctl.sent[n].url, 'http://x').searchParams;
        expect(q.get('label')).toBe('env=prod');
        expect(q.has('sort')).toBe(path.view === 'list');

        emitLabelledCreate(projectId, 'live-prod', 'prod');
        emitLabelledCreate(projectId, 'live-dev', 'dev');
        await flushLive(el);
        ctl.release();
        await settle(el);

        const expected = agents.filter((a) => a.labels?.env === 'prod').length + 1;
        const ids = new Set(internals(el).agents.map((a) => a.id));
        expect(ids.has('live-prod')).toBe(true);
        expect(ids.has('live-dev')).toBe(false);
        expect(internals(el).agentWindow.state).toBe(path.view === 'list' ? 'small' : 'held');
        expect(internals(el).agentStats.total).toBe(expected);
        expect(internals(el).agentWindow.display.every((a) => a.labels?.env === 'prod')).toBe(true);
      });
    }

    it('removing the element during a drain aborts its page fetch and sends no further page request', async () => {
      const projectId = 'p-remove-drain';
      localStorage.setItem('scion-view-project-agents', 'graph');
      const agents = Array.from({ length: 1200 }, (_, i) => makeAgent(i, { projectId }));
      const ctl = controlledFetch(projectId, agents);
      ctl.holdNext();
      const run = vi.spyOn(AgentDrainRunner.prototype, 'run');
      const el = await createComponent(projectId, { holdsFirstLoad: true });
      await vi.waitFor(() => expect(ctl.heldCount).toBe(1));
      expect(ctl.sent).toHaveLength(1);
      // The tree view drains from the first request.
      expect(new URL(ctl.sent[0].url, 'http://x').searchParams.has('sort')).toBe(false);
      expect(ctl.sent[0].signal?.aborted).toBe(false);

      // Removing the element alone does not change the store's scope.
      el.remove();
      expect(ctl.sent[0].signal?.aborted).toBe(true);
      ctl.release();
      // Wait until the aborted drain run has settled with no result.
      expect(run).toHaveBeenCalledTimes(1);
      let outcome: unknown = 'pending';
      void (run.mock.results[0].value as Promise<unknown>).then((r) => (outcome = r));
      await vi.waitFor(() => expect(outcome).toBeNull(), { timeout: 10_000 });
      await el.updateComplete;
      expect(run).toHaveBeenCalledTimes(1);
      expect(ctl.sent).toHaveLength(1);
      expect(stateManager.getAgents().filter((a) => a.projectId === projectId)).toHaveLength(0);
    });

    it('a failed page load that follows earlier data empties the agents instead of keeping the old rows', async () => {
      const projectId = 'p-page-load-fails';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = Array.from({ length: 10 }, (_, i) => makeAgent(i, { projectId }));
      const ctl = controlledFetch(projectId, agents);
      // The project request fails once while the agents request succeeds.
      ctl.failProjectOnce = true;
      const el = await createComponent(projectId);
      await settle(el);
      expect(internals(el).agents).toHaveLength(10);
      expect(internals(el).agentWindow.state).toBe('small');
      const retry = Array.from(el.shadowRoot!.querySelectorAll('sl-button')).find((b) =>
        b.textContent?.includes('Retry')
      ) as HTMLElement | undefined;
      expect(retry).toBeDefined();

      // Retry loads the page again; this time its agents request fails.
      ctl.failAgents = true;
      const n = ctl.requests.length;
      retry!.click();
      await vi.waitFor(() => expect(ctl.requests.length).toBe(n + 1));
      await vi.waitFor(() => expect(el.shadowRoot!.textContent).toContain('Window Project'));
      await settle(el);
      expect(new URL(ctl.requests[n].url, 'http://x').searchParams.get('sort')).toBe('updated');
      expect(internals(el).agents).toEqual([]);
      expect(internals(el).agentWindow.state).toBe('small');
      expect(internals(el).agentStats.total).toBe(0);
      expect(el.shadowRoot!.querySelectorAll('.agent-table-container tbody tr')).toHaveLength(0);
    });
  });

  describe('backend-driven delete (ptone/scion#2483 phase 1b)', () => {
    it('202 keeps the card with Deleting… and no Delete button; SSE deleted removes it', async () => {
      const projectId = 'p-del-202';
      localStorage.setItem('scion-view-project-agents', 'grid');
      const agents = [0, 1, 2].map((i) => makeAgent(i, { projectId }));
      const requests: AgentsRequest[] = [];
      const inner = createFetchHandler({
        projectId,
        projectCaps: { actions: ['read'] },
        agents,
        requests,
      });
      const deletion = {
        state: 'deleting',
        soft: false,
        claim: 1,
        startedAt: new Date().toISOString(),
        leaseExpiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
      };
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        if (init?.method === 'DELETE') {
          return Promise.resolve(jsonResponse({ agentId: 'a-1', deletion }, 202));
        }
        return inner(input, init);
      });

      const el = await createComponent(projectId);
      const trashButtons = (): number =>
        el.shadowRoot?.querySelectorAll('sl-icon[name="trash"]').length ?? 0;
      expect(trashButtons()).toBe(3);

      const page = el as unknown as {
        handleAgentAction(id: string, action: string, event?: MouseEvent): Promise<void>;
      };
      await page.handleAgentAction('a-1', 'delete', { altKey: true } as MouseEvent);
      await new Promise((r) => setTimeout(r, 150)); // state.ts flush fallback
      await el.updateComplete;

      const ids = (): string[] =>
        internals(el)
          .agents.map((a) => a.id)
          .sort();
      expect(ids()).toEqual(['a-0', 'a-1', 'a-2']);
      const badges = [...(el.shadowRoot?.querySelectorAll('scion-deletion-badge') ?? [])] as Array<
        HTMLElement & { updateComplete: Promise<boolean> }
      >;
      await Promise.all(badges.map((b) => b.updateComplete));
      const labels = badges
        .map((b) => b.shadowRoot?.querySelector('.badge')?.textContent?.trim())
        .filter(Boolean);
      expect(labels).toEqual(['Deleting…']);
      expect(trashButtons()).toBe(2);

      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({ subject: 'agent.a-1.deleted', data: {} });
      await new Promise((r) => setTimeout(r, 150));
      await el.updateComplete;
      expect(ids()).toEqual(['a-0', 'a-2']);
    });

    const sseUpdate = (subject: string, data: unknown): void =>
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({ subject, data });
    const waitMs = (ms: number) => new Promise((r) => setTimeout(r, ms));

    /** Badge labels and icon counts inside table rows only (list view). */
    async function rowState(
      el: TestEl
    ): Promise<{ badges: string[]; banners: string[]; icons: (n: string) => number }> {
      const rows = [...(el.shadowRoot?.querySelectorAll('tbody tr') ?? [])];
      const badges = rows.flatMap((r) => [
        ...(r.querySelectorAll('scion-deletion-badge') as NodeListOf<
          HTMLElement & { updateComplete: Promise<boolean> }
        >),
      ]);
      const banners = rows.flatMap((r) => [
        ...(r.querySelectorAll('scion-deletion-banner') as NodeListOf<
          HTMLElement & { updateComplete: Promise<boolean> }
        >),
      ]);
      await Promise.all([...badges, ...banners].map((b) => b.updateComplete));
      return {
        badges: badges
          .map((b) => b.shadowRoot?.querySelector('.badge')?.textContent?.trim() ?? '')
          .filter(Boolean),
        // Phase 2: a failed view shows the compact failure banner instead.
        banners: banners
          .map((b) => b.shadowRoot?.querySelector('.title')?.textContent?.trim() ?? '')
          .filter(Boolean),
        icons: (n: string) =>
          rows.reduce((sum, r) => sum + r.querySelectorAll(`sl-icon[name="${n}"]`).length, 0),
      };
    }

    const lifecycleCaps = { actions: ['read', 'update', 'delete', 'lifecycle'] };

    it('list view (table rows): Deleting… badge, and Stop/Delete hidden on that row only', async () => {
      const projectId = 'p-del-rows';
      localStorage.setItem('scion-view-project-agents', 'list');
      const agents = [0, 1, 2].map((i) =>
        makeAgent(i, { projectId, _capabilities: lifecycleCaps })
      );
      stubFetch(
        createFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests: [],
        })
      );
      const el = await createComponent(projectId);
      let st = await rowState(el);
      expect(st.icons('trash')).toBe(3);
      expect(st.icons('stop-circle')).toBe(3);

      sseUpdate('agent.a-1.status', {
        deletion: {
          state: 'deleting',
          soft: false,
          claim: 1,
          startedAt: new Date().toISOString(),
          leaseExpiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
        },
      });
      await waitMs(150);
      await el.updateComplete;
      st = await rowState(el);
      expect(st.badges).toEqual(['Deleting…']);
      expect(st.icons('trash')).toBe(2);
      expect(st.icons('stop-circle')).toBe(2);
    });

    it('paged list: the lease timer watches agentWindow.items and flips the row to Delete interrupted', async () => {
      const projectId = 'p-del-paged';
      localStorage.setItem('scion-view-project-agents', 'list');
      localStorage.setItem(
        `scion-sort-project-agents-${projectId}`,
        JSON.stringify({ field: 'updated', dir: 'desc' })
      );
      const agents = Array.from({ length: PROJECT_AGENTS_FIT_THRESHOLD + 1 }, (_, i) =>
        makeAgent(i, { projectId, _capabilities: lifecycleCaps })
      );
      stubFetch(
        createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests: [],
        })
      );
      const el = await createComponent(projectId);
      expect(internals(el).agentWindow.state).toBe('paged');
      expect(internals(el).agents).toEqual([]); // paged: rows come from the window only
      const target = internals(el).agentWindow.items[0].id;

      // Fake clock only after mount (mount waits on real timers). The lease
      // is far enough out that nothing but the controller's timer, driven
      // by advanceTimersByTime, can flip it.
      let st: Awaited<ReturnType<typeof rowState>>;
      vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'Date'] });
      try {
        sseUpdate(`agent.${target}.status`, {
          deletion: {
            state: 'deleting',
            soft: false,
            claim: 1,
            startedAt: new Date().toISOString(),
            leaseExpiresAt: new Date(Date.now() + 20_000).toISOString(),
          },
        });
        vi.advanceTimersByTime(150); // state.ts flush fallback
        await el.updateComplete;
        expect((await rowState(el)).badges).toEqual(['Deleting…']);

        vi.advanceTimersByTime(19_000);
        await el.updateComplete;
        expect((await rowState(el)).badges).toEqual(['Deleting…']);

        // Nothing else re-renders the page: only the controller's timer can.
        vi.advanceTimersByTime(1_000);
        await el.updateComplete;
        st = await rowState(el);
      } finally {
        vi.useRealTimers();
      }
      expect(st.badges).toEqual([]);
      expect(st.banners).toEqual(['Delete interrupted']);
      expect(st.icons('trash')).toBe(internals(el).agentWindow.items.length);
    });
  });

  describe('phase 2: shared delete helper, failure banner, stopping filter (ptone/scion#2483)', () => {
    const lifecycleCaps = { actions: ['read', 'update', 'delete', 'lifecycle'] };
    const base = { soft: false, claim: 1, startedAt: new Date().toISOString() };
    const sseUpdate = (subject: string, data: unknown): void =>
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({ subject, data });
    const flushState = (): Promise<unknown> => new Promise((r) => setTimeout(r, 150));
    type BannerEl = HTMLElement & { updateComplete: Promise<boolean> };
    type Page = {
      handleAgentAction(id: string, action: string, event?: MouseEvent): Promise<void>;
    };

    async function banners(el: TestEl, scope: string): Promise<BannerEl[]> {
      const list = [
        ...(el.shadowRoot?.querySelectorAll(`${scope} scion-deletion-banner`) ?? []),
      ] as BannerEl[];
      await Promise.all(list.map((b) => b.updateComplete));
      return list.filter((b) => !b.hasAttribute('hidden'));
    }
    const title = (b: BannerEl): string =>
      b.shadowRoot?.querySelector('.title')?.textContent?.trim() ?? '';

    /** A small project; mutations (non-GET) are recorded and answered by `onMutate`. */
    async function mountProject(
      projectId: string,
      agents: Agent[],
      view: 'grid' | 'list',
      onMutate: (url: string) => Response = () => new Response(null, { status: 204 })
    ): Promise<{ el: TestEl; mutations: string[] }> {
      localStorage.setItem('scion-view-project-agents', view);
      const mutations: string[] = [];
      const inner = createFetchHandler({
        projectId,
        projectCaps: { actions: ['read'] },
        agents,
        requests: [],
      });
      stubFetch((input: string | URL | Request, init?: RequestInit) => {
        const url = requestUrl(input);
        if (init?.method && init.method !== 'GET') {
          mutations.push(`${init.method} ${url}`);
          return Promise.resolve(onMutate(url));
        }
        return inner(input, init);
      });
      const el = await createComponent(projectId);
      return { el, mutations };
    }

    beforeEach(() => {
      vi.mocked(runAgentDelete).mockClear();
      vi.mocked(showConfirm).mockClear();
      vi.mocked(showToast).mockClear();
    });

    it('Delete delegates to the shared helper; the page sends no DELETE itself', async () => {
      const agents = [0, 1].map((i) =>
        makeAgent(i, { projectId: 'p2-deleg', _capabilities: lifecycleCaps })
      );
      const { el, mutations } = await mountProject('p2-deleg', agents, 'grid');
      vi.mocked(runAgentDelete).mockResolvedValueOnce({ kind: 'deleted', forced: false });
      const click = { altKey: true } as MouseEvent;
      await (el as unknown as Page).handleAgentAction('a-1', 'delete', click);
      await el.updateComplete;
      expect(runAgentDelete).toHaveBeenCalledTimes(1);
      expect(runAgentDelete).toHaveBeenCalledWith(
        expect.objectContaining({ agentId: 'a-1', agentName: 'agent-1', event: click })
      );
      expect(mutations).toEqual([]);
      expect(internals(el).agents.map((a) => a.id)).toEqual(['a-0']);
    });

    it('a 502 now offers the force fallback (the helper owns it), and a failure is toasted', async () => {
      const agents = [makeAgent(0, { projectId: 'p2-502', _capabilities: lifecycleCaps })];
      const { el, mutations } = await mountProject('p2-502', agents, 'grid', () =>
        jsonResponse({ error: { code: 'runtime_error', message: 'broker down' } }, 502)
      );
      vi.mocked(showConfirm).mockResolvedValueOnce(false); // decline force
      await (el as unknown as Page).handleAgentAction('a-0', 'delete', {
        altKey: true,
      } as MouseEvent);
      expect(showConfirm).toHaveBeenCalledTimes(1);
      expect(vi.mocked(showConfirm).mock.calls[0][0]).toMatch(/Force delete this agent\?/);
      expect(mutations).toEqual(['DELETE /api/v1/agents/a-0']);
      expect(showToast).toHaveBeenCalledWith('broker down');
    });

    const codes: Array<[string, Record<string, unknown>, string]> = [
      [
        'runtime_error',
        { code: 'runtime_error', error: 'broker refused' },
        'Delete failed: broker refused',
      ],
      ['conflict', { code: 'conflict' }, 'Delete failed: conflict'],
      ['abandoned', { code: 'abandoned' }, 'Delete interrupted'],
      [
        'revoke_failed',
        { code: 'revoke_failed', stage: 'finalizing' },
        'Delete failed: could not revoke credentials',
      ],
      [
        'finalize_failed',
        { code: 'finalize_failed', stage: 'finalizing' },
        'Delete failed: could not finalize',
      ],
      ['in_doubt', { code: 'in_doubt' }, 'Delete failed: outcome unknown'],
    ];
    for (const [name, extra, expected] of codes) {
      it(`${name}: the card (full) and the row (compact) show the failure banner with Retry and Force`, async () => {
        for (const view of ['grid', 'list'] as const) {
          const projectId = `p2-${name}-${view}`;
          const agents = [0, 1].map((i) =>
            makeAgent(i, { projectId, _capabilities: lifecycleCaps })
          );
          const { el } = await mountProject(projectId, agents, view);
          sseUpdate('agent.a-0.status', { deletion: { ...base, state: 'failed', ...extra } });
          await flushState();
          await el.updateComplete;
          const list = await banners(el, view === 'grid' ? '.agent-card' : 'tbody tr');
          expect(list.map(title)).toEqual([expected]);
          expect(list[0].hasAttribute('compact')).toBe(view === 'list');
          expect(list[0].shadowRoot?.querySelector('.force')?.getAttribute('aria-label')).toBe(
            'Force delete agent-0'
          );
          expect(list[0].shadowRoot?.querySelector('.retry')).not.toBeNull();
          expect(list[0].shadowRoot?.querySelector('.force')).not.toBeNull();
          el.remove();
        }
      });
    }

    it('a client-flipped abandoned view shows the banner; a live deleting view shows none (no Force)', async () => {
      const agents = [0, 1].map((i) =>
        makeAgent(i, { projectId: 'p2-flip', _capabilities: lifecycleCaps })
      );
      const { el } = await mountProject('p2-flip', agents, 'grid');
      sseUpdate('agent.a-0.status', {
        deletion: {
          ...base,
          state: 'deleting',
          leaseExpiresAt: new Date(Date.now() + 600_000).toISOString(),
        },
      });
      sseUpdate('agent.a-1.status', {
        deletion: {
          ...base,
          state: 'deleting',
          leaseExpiresAt: new Date(Date.now() - 1_000).toISOString(),
        },
      });
      await flushState();
      await el.updateComplete;
      const list = await banners(el, '.agent-card');
      expect(list.map(title)).toEqual(['Delete interrupted']);
    });

    it('Retry calls the helper without a confirm; Force with force:true and ?force=true', async () => {
      const agents = [makeAgent(0, { projectId: 'p2-btn', _capabilities: lifecycleCaps })];
      const { el, mutations } = await mountProject('p2-btn', agents, 'list');
      sseUpdate('agent.a-0.status', { deletion: { ...base, state: 'failed', code: 'in_doubt' } });
      await flushState();
      await el.updateComplete;
      const [banner] = await banners(el, 'tbody tr');

      (banner.shadowRoot?.querySelector('.retry') as HTMLElement).click();
      await vi.waitFor(() => expect(runAgentDelete).toHaveBeenCalledTimes(1));
      await vi.mocked(runAgentDelete).mock.results[0].value;
      expect(vi.mocked(runAgentDelete).mock.calls[0][0]).toMatchObject({
        agentId: 'a-0',
        confirm: false,
      });
      expect(vi.mocked(runAgentDelete).mock.calls[0][0].force).toBeUndefined();
      expect(showConfirm).not.toHaveBeenCalled();

      (banner.shadowRoot?.querySelector('.force') as HTMLElement).click();
      await vi.waitFor(() => expect(runAgentDelete).toHaveBeenCalledTimes(2));
      await vi.mocked(runAgentDelete).mock.results[1].value;
      expect(vi.mocked(runAgentDelete).mock.calls[1][0]).toMatchObject({
        agentId: 'a-0',
        force: true,
      });
      expect(showConfirm).toHaveBeenCalledTimes(1);
      expect(mutations).toEqual([
        'DELETE /api/v1/agents/a-0',
        'DELETE /api/v1/agents/a-0?force=true',
      ]);
    });

    it('Start answered 409 delete_in_progress shows the explanation', async () => {
      const agents = [
        makeAgent(0, { projectId: 'p2-start', phase: 'stopped', _capabilities: lifecycleCaps }),
      ];
      const { el } = await mountProject('p2-start', agents, 'grid', () =>
        jsonResponse({ error: { code: 'delete_in_progress', message: 'being deleted' } }, 409)
      );
      await (el as unknown as Page).handleAgentAction('a-0', 'start');
      expect(showToast).toHaveBeenCalledWith(START_BLOCKED_BY_DELETE_MESSAGE);
    });

    it('a persisted stopping filter is restored (review nit 6)', async () => {
      const projectId = 'p2-filter-persisted';
      localStorage.setItem(`scion-filter-project-agents-phase-${projectId}`, 'stopping');
      const agents = [makeAgent(0, { projectId, phase: 'stopping' }), makeAgent(1, { projectId })];
      const { el } = await mountProject(projectId, agents, 'grid');
      await vi.waitFor(() =>
        expect(internals(el).agentWindow.items.map((a) => a.id)).toEqual(['a-0'])
      );
      const active = el.shadowRoot?.querySelector('.filter-bar button.active');
      expect(active?.textContent?.trim()).toBe('Stopping');
    });

    it('the stopping filter shows stopping agents, and live deltas move agents in and out', async () => {
      const projectId = 'p2-filter';
      const agents = [
        makeAgent(0, { projectId, phase: 'stopping' }),
        makeAgent(1, { projectId }),
        makeAgent(2, { projectId, phase: 'stopped' }),
      ];
      const { el } = await mountProject(projectId, agents, 'grid');
      const shown = (): string[] =>
        internals(el)
          .agentWindow.items.map((a) => a.id)
          .sort();
      const button = [...(el.shadowRoot?.querySelectorAll('.filter-bar button') ?? [])].find(
        (b) => b.textContent?.trim() === 'Stopping'
      ) as HTMLElement | undefined;
      expect(button).toBeDefined();
      button!.click();
      await el.updateComplete;
      expect(localStorage.getItem(`scion-filter-project-agents-phase-${projectId}`)).toBe(
        'stopping'
      );
      await vi.waitFor(() => expect(shown()).toEqual(['a-0']));

      sseUpdate('agent.a-1.status', { phase: 'stopping' });
      await flushState();
      await el.updateComplete;
      expect(shown()).toEqual(['a-0', 'a-1']);

      sseUpdate('agent.a-0.status', { phase: 'stopped' });
      await flushState();
      await el.updateComplete;
      expect(shown()).toEqual(['a-1']);
      expect(el.shadowRoot?.querySelectorAll('.agent-card').length).toBe(1);
    });
  });

  describe('a delete the hub accepts with a 202, in every window state', () => {
    const lifecycleCaps = { actions: ['read', 'update', 'delete', 'lifecycle'] };
    const deletingView = (): DeletionInfo => ({
      state: 'deleting',
      soft: false,
      claim: 1,
      startedAt: new Date().toISOString(),
      leaseExpiresAt: new Date(Date.now() + 10 * 60_000).toISOString(),
    });
    const altClick = { altKey: true } as MouseEvent;
    const sseUpdate = (subject: string, data: unknown): void =>
      (
        stateManager as unknown as { handleUpdate(u: { subject: string; data: unknown }): void }
      ).handleUpdate({ subject, data });

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

    function icons(row: Element | null, name: string): number {
      return row?.querySelectorAll(`sl-icon[name="${name}"]`).length ?? 0;
    }

    interface StateRow {
      state: 'small' | 'held' | 'paged';
      count: number;
      /** Load in the tree view (complete-needing), then switch to the row view. */
      viaTree: boolean;
    }
    const states: StateRow[] = [
      { state: 'small', count: 30, viaTree: false },
      { state: 'held', count: 1200, viaTree: true },
      { state: 'paged', count: PROJECT_AGENTS_FIT_THRESHOLD + 1, viaTree: false },
    ];
    // A capped set exists only in the tree view here: switching to the grid
    // or list sends a sorted request and ends paged.

    describe.each(
      states.flatMap((st) => (['list', 'grid'] as const).map((view) => ({ ...st, view })))
    )('$state, $view view', ({ state, count, viaTree, view }) => {
      it('keeps the row with Deleting and its actions hidden, sends no list request, and the live delete removes it', async () => {
        const projectId = `p-del-202-${state}-${view}`;
        localStorage.setItem('scion-view-project-agents', viaTree ? 'graph' : view);
        localStorage.setItem(
          `scion-sort-project-agents-${projectId}`,
          JSON.stringify({ field: 'updated', dir: 'desc' })
        );
        const agents = Array.from({ length: count }, (_, i) =>
          makeAgent(i, { projectId, _capabilities: lifecycleCaps, deletion: null })
        );
        const requests: AgentsRequest[] = [];
        const inner = createRealisticFetchHandler({
          projectId,
          projectCaps: { actions: ['read'] },
          agents,
          requests,
        });
        const deletes: string[] = [];
        stubFetch((input: string | URL | Request, init?: RequestInit) => {
          if (init?.method === 'DELETE') {
            deletes.push(requestUrl(input));
            return Promise.resolve(jsonResponse({ deletion: deletingView() }, 202));
          }
          return inner(input, init);
        });
        const el = await createComponent(projectId);
        if (viaTree) {
          await vi.waitFor(() => expect(internals(el).agentWindow.state).toBe(state));
          viewToggle(el)!.dispatchEvent(new CustomEvent('view-change', { detail: { view } }));
          await settle(el);
        }
        const win = internals(el).agentWindow;
        expect(win.state).toBe(state);
        const id = win.items[0].id;
        const rowsBefore = win.items.length;
        const statsBefore = { ...internals(el).agentStats };
        const requestsBefore = requests.length;
        expect(icons(rowOf(el, id), 'trash')).toBe(1);
        expect(icons(rowOf(el, id), 'stop-circle')).toBe(1);

        await (
          el as unknown as {
            handleAgentAction(id: string, action: string, event?: MouseEvent): Promise<void>;
          }
        ).handleAgentAction(id, 'delete', altClick);
        await flushLive(el);

        expect(deletes).toEqual([`/api/v1/agents/${id}`]);
        expect(win.items).toHaveLength(rowsBefore);
        expect(win.items.find((a) => a.id === id)?.deletion?.state).toBe('deleting');
        expect(await badgeText(rowOf(el, id))).toBe('Deleting…');
        expect(icons(rowOf(el, id), 'trash')).toBe(0);
        expect(icons(rowOf(el, id), 'stop-circle')).toBe(0);
        expect(icons(rowOf(el, win.items[1].id), 'trash')).toBe(1);
        expect(win.updatesAvailable).toBe(false);
        expect(internals(el).agentStats).toEqual(statsBefore);
        await settle(el);
        expect(requests.length).toBe(requestsBefore);

        sseUpdate(`agent.${id}.deleted`, {});
        await flushLive(el);
        expect(win.items.some((a) => a.id === id)).toBe(false);
        expect(rowOf(el, id)).toBeNull();
        await settle(el);
        expect(requests.length).toBe(requestsBefore);
      });
    });
  });
}, 20_000);

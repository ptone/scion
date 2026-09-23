/**
 * REPRO (investigation nc-member-sort-inv) — not a production test.
 *
 * An agent that the shared stateManager still holds (stale seed, SSE
 * `deleted` missed) but the space members endpoint no longer returns makes
 * the members sidebar flicker: every SSE agent tick re-adds it (and schedules
 * a 1 s canAttach refetch), the refetch removes it, the next tick re-adds it...
 */

// @vitest-environment happy-dom

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { apiFetch } from '../../client/api.js';

/* eslint-disable @typescript-eslint/no-explicit-any */

const fakeState = vi.hoisted(() => {
  const t = new EventTarget() as any;
  t.agents = new Map<string, any>();
  t.getAgents = () => Array.from(t.agents.values());
  t.getDeletedAgentIds = () => new Set<string>();
  t.seedAgents = (list: any[]) => list.forEach((a) => t.agents.set(a.id, a)); // additive, like the real one
  return t;
});

vi.mock('../../client/main.js', () => ({ navigateTo: vi.fn(), stateManager: fakeState }));
vi.mock('../../client/api.js', async (orig) => ({
  ...(await orig<typeof import('../../client/api.js')>()),
  apiFetch: vi.fn(),
}));

beforeAll(async () => {
  await import('./chat.js');
});
afterEach(() => vi.useRealTimers());

const live = { id: 'live', kind: 'agent', displayName: 'custom-auth-proxy-lead', projectId: 'p1', phase: 'running', activity: 'executing' };
const ghost = { id: 'ghost', name: 'ci-compat-literals-dev', slug: 'ci-compat-literals-dev', projectId: 'p1', phase: 'running', activity: 'blocked' };

describe('member list flicker repro', () => {
  it('ghost agent toggles in and out on every SSE tick + refetch', async () => {
    vi.useFakeTimers();
    // Members endpoint never returns the ghost (it was deleted server-side).
    vi.mocked(apiFetch).mockImplementation(async () =>
      new Response(JSON.stringify({ humans: [], agents: [live] }), { status: 200 })
    );
    fakeState.agents.set('ghost', ghost); // stale entry from an earlier seed
    fakeState.agents.set('live', { ...live, name: live.displayName });

    const page = document.createElement('scion-page-chat') as any;
    page.v2Conversation = { projectId: 'p1', isDM: false };
    page.v2AgentMembers = [live];

    const trace: boolean[] = [];
    const has = () => page.v2AgentMembers.some((a: any) => a.id === 'ghost');
    for (let i = 0; i < 3; i++) {
      page._handleAgentsUpdated(); // SSE status tick from ANY agent
      trace.push(has());
      await vi.advanceTimersByTimeAsync(1100); // canAttach refetch fires
      trace.push(has());
    }
    console.log('ghost visible trace:', trace, 'members fetches:', vi.mocked(apiFetch).mock.calls.length);
    expect(trace).toEqual([true, false, true, false, true, false]);
  });
});

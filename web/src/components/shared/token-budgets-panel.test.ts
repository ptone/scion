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

import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import type { ScionTokenBudgetsPanel } from './token-budgets-panel.js';
import {
  formatTokenAmount,
  type GroupBudgetItem,
  type UsageBudgetEntry,
} from '../../shared/usage-budgets.js';

function makeBudget(overrides: Partial<UsageBudgetEntry> = {}): UsageBudgetEntry {
  return {
    id: 'budget-1',
    limit_definition_id: 'def-1',
    limit_name: 'llm_tokens_per_project_day',
    scope_kind: 'project',
    scope_id: 'proj-1',
    window_start_utc: '2026-11-28T00:00:00Z',
    window_end_utc: '2026-11-29T00:00:00Z',
    cap: 1_000_000,
    token_action: 'pause_new',
    consumed: 250_000,
    state: 'ok',
    thresholds_notified: 0,
    override_until: null,
    state_version: 3,
    as_of: '2026-11-28T12:00:00Z',
    ...overrides,
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('scion-token-budgets-panel', () => {
  let el: ScionTokenBudgetsPanel | null = null;

  beforeAll(async () => {
    await import('./token-budgets-panel.js');
  }, 60_000);

  afterEach(() => {
    el?.remove();
    el = null;
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('renders ok, warn, and exhausted state badges and unlimited caps', async () => {
    el = document.createElement('scion-token-budgets-panel') as ScionTokenBudgetsPanel;
    el.budgets = [
      makeBudget({ id: 'b-ok', state: 'ok', consumed: 200_000, cap: 1_000_000 }),
      makeBudget({
        id: 'b-warn',
        limit_name: 'llm_tokens_per_project_month',
        state: 'warn',
        consumed: 850_000,
        cap: 1_000_000,
        token_action: 'suspend_and_pause',
      }),
      makeBudget({
        id: 'b-exh',
        limit_name: 'llm_tokens_per_user_day',
        state: 'exhausted',
        consumed: 1_200_000,
        cap: 1_000_000,
        token_action: 'notify_only',
      }),
      makeBudget({
        id: '',
        limit_name: 'llm_tokens_per_user_month',
        state: 'ok',
        consumed: 50_000,
        cap: null,
      }),
    ];
    document.body.appendChild(el);
    await el.updateComplete;

    const badges = Array.from(
      el.shadowRoot?.querySelectorAll('[data-testid="budget-state-badge"]') ?? []
    );
    expect(badges.map((b) => b.getAttribute('data-state'))).toEqual([
      'ok',
      'warn',
      'exhausted',
      'ok',
    ]);
    expect(badges.map((b) => b.getAttribute('variant'))).toEqual([
      'success',
      'warning',
      'danger',
      'success',
    ]);

    const unlimited = el.shadowRoot?.querySelector('[data-testid="budget-cap-unlimited"]');
    expect(unlimited?.textContent).toContain('Unlimited');

    const progressBars = el.shadowRoot?.querySelectorAll('sl-progress-bar');
    // Only the 3 finite-cap rows get a progress bar; unlimited row has none.
    expect(progressBars?.length).toBe(3);
  });

  it('submits override with state_version and If-Match and handles 412 conflict', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ error: 'conflict' }, 412))
      .mockResolvedValueOnce(jsonResponse(makeBudget({ state_version: 4 }), 200));
    vi.stubGlobal('fetch', fetchMock);

    el = document.createElement('scion-token-budgets-panel') as ScionTokenBudgetsPanel;
    el.budgets = [makeBudget({ state: 'exhausted', consumed: 1_000_000, state_version: 7 })];
    el.canOverride = true;
    el.overrideEndpointBase = '/api/v1/projects/proj-1/usage/budgets';
    const refreshSpy = vi.fn();
    el.addEventListener('budgets-refresh', refreshSpy);
    document.body.appendChild(el);
    await el.updateComplete;

    // Open override dialog
    const openBtn = el.shadowRoot?.querySelector(
      '[data-testid="budget-override-btn"]'
    ) as HTMLElement;
    expect(openBtn).not.toBeNull();
    openBtn.click();
    await el.updateComplete;

    // Submit -> hits 412 conflict
    const submitBtn = el.shadowRoot?.querySelector(
      '[data-testid="budget-override-submit-btn"]'
    ) as HTMLElement;
    submitBtn.click();
    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
    await el.updateComplete;

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe('/api/v1/projects/proj-1/usage/budgets/budget-1:override');
    expect(init.method).toBe('POST');
    const headers = init.headers as Record<string, string>;
    expect(headers['If-Match']).toBe('"7"');
    const parsedBody = JSON.parse(typeof init.body === 'string' ? init.body : '{}') as {
      override_until: string;
      state_version: number;
    };
    expect(parsedBody.state_version).toBe(7);
    expect(typeof parsedBody.override_until).toBe('string');

    // 412 should surface error and request refresh
    await vi.waitFor(() => {
      const errAlert = el?.shadowRoot?.querySelector('[data-testid="budget-override-error"]');
      expect(errAlert?.textContent).toContain('version conflict');
    });
    expect(refreshSpy).toHaveBeenCalledTimes(1);
  });

  it('clears active override with explicit null override_until and handles 428', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ error: 'precondition required' }, 428))
      .mockResolvedValueOnce(jsonResponse(makeBudget({ override_until: null }), 200));
    vi.stubGlobal('fetch', fetchMock);

    el = document.createElement('scion-token-budgets-panel') as ScionTokenBudgetsPanel;
    el.budgets = [
      makeBudget({
        override_until: '2026-11-28T20:00:00Z',
        as_of: '2026-11-28T12:00:00Z',
        state_version: 5,
      }),
    ];
    el.canOverride = true;
    el.overrideEndpointBase = '/api/v1/projects/proj-1/usage/budgets';
    const refreshSpy = vi.fn();
    el.addEventListener('budgets-refresh', refreshSpy);
    document.body.appendChild(el);
    await el.updateComplete;

    expect(el.shadowRoot?.querySelector('[data-testid="budget-override-badge"]')).not.toBeNull();
    const clearBtn = el.shadowRoot?.querySelector(
      '[data-testid="budget-clear-override-btn"]'
    ) as HTMLElement;
    expect(clearBtn).not.toBeNull();
    clearBtn.click();

    await vi.waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
    await el.updateComplete;

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    const body = JSON.parse(typeof init.body === 'string' ? init.body : '{}') as Record<
      string,
      unknown
    >;
    expect(body).toEqual({ override_until: null, state_version: 5 });
    await vi.waitFor(() => {
      expect(
        el?.shadowRoot?.querySelector('[data-testid="budget-inline-error"]')?.textContent
      ).toContain('428');
    });
    expect(refreshSpy).toHaveBeenCalledTimes(1);
  });

  it('renders non-additive notice and per-member breakdown for pooled groups', async () => {
    const groupItem: GroupBudgetItem = {
      ...makeBudget({
        limit_name: 'llm_tokens_per_group_month',
        scope_kind: 'group',
        scope_id: 'grp-1',
      }),
      non_additive: true,
      members: [
        {
          user_id: 'u-1',
          email: 'alice@example.com',
          display_name: 'Alice',
          tokens_summable: 150_000,
          calls: 12,
          events: 4,
        },
      ],
    };

    el = document.createElement('scion-token-budgets-panel') as ScionTokenBudgetsPanel;
    el.budgets = [groupItem];
    document.body.appendChild(el);
    await el.updateComplete;

    expect(
      el.shadowRoot?.querySelector('[data-testid="budget-non-additive-notice"]')
    ).not.toBeNull();
    const breakdown = el.shadowRoot?.querySelector('[data-testid="budget-member-breakdown"]');
    expect(breakdown).not.toBeNull();
    expect(breakdown?.textContent).toContain('Alice');
    expect(breakdown?.textContent).toContain('150K');
  });

  it('computes override presets from current time when as_of is stale and formats 999,950 as 1M', async () => {
    expect(formatTokenAmount(999_950)).toBe('1M');
    expect(formatTokenAmount(999_999)).toBe('1M');

    const nowMs = Date.now();
    const staleAsOf = new Date(nowMs - 4 * 3600_000).toISOString();
    const windowEnd = new Date(nowMs + 12 * 3600_000).toISOString();

    el = document.createElement('scion-token-budgets-panel') as ScionTokenBudgetsPanel;
    el.budgets = [
      makeBudget({
        state: 'exhausted',
        as_of: staleAsOf,
        window_end_utc: windowEnd,
      }),
    ];
    el.canOverride = true;
    el.overrideEndpointBase = '/api/v1/projects/proj-1/usage/budgets';
    document.body.appendChild(el);
    await el.updateComplete;

    const openBtn = el.shadowRoot?.querySelector(
      '[data-testid="budget-override-btn"]'
    ) as HTMLElement;
    openBtn.click();
    await el.updateComplete;

    const untilInput = el.shadowRoot?.querySelector(
      '[data-testid="budget-override-until-input"]'
    ) as HTMLInputElement | null;
    const chosenMs = Date.parse(untilInput?.getAttribute('value') ?? '');
    // Default +1h preset must be in the future relative to Date.now(), not stale as_of.
    expect(chosenMs).toBeGreaterThan(nowMs + 50 * 60_000);
  });
});

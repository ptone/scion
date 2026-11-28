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
import {
  resetServerFlagStateForTests,
  setFeatureFlag,
  TOKENOMICS_TOKEN_BUDGETS_FLAG,
  TOKENOMICS_USAGE_LEDGER_FLAG,
} from '../../utils/feature-flags.js';

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const TEST_GROUP = {
  id: 'grp-1',
  name: 'Platform Team',
  slug: 'platform-team',
  groupType: 'explicit',
  description: 'Core team',
  ownerId: 'user-1',
  created: '2026-11-01T00:00:00Z',
  updated: '2026-11-28T00:00:00Z',
};

describe('scion-page-admin-group-detail — pooled token budgets', () => {
  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./admin-group-detail.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    delete window.__SCION_FEATURES__;
    resetServerFlagStateForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('hides pooled budgets and skips fetch when experiment is off', async () => {
    window.history.pushState({}, '', '/admin/groups/grp-1');
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, false);

    const fetchMock = vi.fn((url: string | URL | Request) => {
      const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
      if (path.endsWith('/api/v1/groups/grp-1')) {
        return Promise.resolve(jsonResponse(TEST_GROUP));
      }
      if (path.includes('/members')) {
        return Promise.resolve(jsonResponse({ members: [] }));
      }
      if (path.includes('/users/user-1')) {
        return Promise.resolve(jsonResponse({ id: 'user-1', displayName: 'Owner' }));
      }
      return Promise.resolve(jsonResponse({}, 404));
    });
    vi.stubGlobal('fetch', fetchMock);

    element = document.createElement('scion-page-admin-group-detail') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(element?.shadowRoot?.querySelector('scion-group-member-editor')).not.toBeNull();
    });

    const calledBudgets = fetchMock.mock.calls.some(([u]) => {
      const s = typeof u === 'string' ? u : u instanceof URL ? u.href : u.url;
      return s.includes('/usage/budgets');
    });
    expect(calledBudgets).toBe(false);
    expect(
      element.shadowRoot?.querySelector('[data-testid="group-token-budgets-section"]')
    ).toBeNull();
  });

  it('loads pooled group budgets with non-additive notice and per-member breakdown when enabled', async () => {
    window.history.pushState({}, '', '/admin/groups/grp-1');
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);

    const fetchMock = vi.fn((url: string | URL | Request) => {
      const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
      if (path.endsWith('/api/v1/groups/grp-1')) {
        return Promise.resolve(jsonResponse(TEST_GROUP));
      }
      if (path.endsWith('/api/v1/groups/grp-1/usage/budgets')) {
        return Promise.resolve(
          jsonResponse({
            scope: { kind: 'group', id: 'grp-1' },
            group_name: 'Platform Team',
            non_additive: true,
            as_of: '2026-11-28T12:00:00Z',
            budgets: [
              {
                id: 'gb-1',
                limit_definition_id: 'def-g',
                limit_name: 'llm_tokens_per_group_month',
                scope_kind: 'group',
                scope_id: 'grp-1',
                window_start_utc: '2026-11-01T00:00:00Z',
                window_end_utc: '2026-12-01T00:00:00Z',
                cap: 10_000_000,
                token_action: 'pause_new',
                consumed: 4_000_000,
                state: 'ok',
                thresholds_notified: 0,
                override_until: null,
                state_version: 1,
                as_of: '2026-11-28T12:00:00Z',
                non_additive: true,
                members: [
                  {
                    user_id: 'u-1',
                    display_name: 'Alice',
                    email: 'alice@example.com',
                    tokens_summable: 4_000_000,
                    calls: 40,
                    events: 10,
                  },
                ],
              },
            ],
          })
        );
      }
      if (path.includes('/members')) {
        return Promise.resolve(jsonResponse({ members: [] }));
      }
      if (path.includes('/users/user-1')) {
        return Promise.resolve(jsonResponse({ id: 'user-1', displayName: 'Owner' }));
      }
      return Promise.resolve(jsonResponse({}, 404));
    });
    vi.stubGlobal('fetch', fetchMock);

    element = document.createElement('scion-page-admin-group-detail') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      const panel = element?.shadowRoot?.querySelector('scion-token-budgets-panel');
      expect(panel).not.toBeNull();
      const notice = panel?.shadowRoot?.querySelector('[data-testid="budget-non-additive-notice"]');
      expect(notice).not.toBeNull();
      const breakdown = panel?.shadowRoot?.querySelector('[data-testid="budget-member-breakdown"]');
      expect(breakdown?.textContent).toContain('Alice');
    });
  });
});

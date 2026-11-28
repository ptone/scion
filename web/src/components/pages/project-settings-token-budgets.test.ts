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

const TEST_PROJECT = {
  id: 'proj-1',
  name: 'Alpha',
  slug: 'alpha',
  _capabilities: { actions: ['read', 'update', 'delete', 'manage'] },
};

describe('scion-page-project-settings — token budgets & usage.unmeasured_policy', () => {
  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./project-settings.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    delete window.__SCION_FEATURES__;
    resetServerFlagStateForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function stubProjectSettingsFetch(putBodies: Array<Record<string, unknown>>) {
    return vi.fn((url: string | URL | Request, init?: RequestInit): Promise<Response> => {
      const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
      const method = (init?.method || 'GET').toUpperCase();
      if (path.endsWith('/api/v1/projects/proj-1')) {
        return Promise.resolve(jsonResponse(TEST_PROJECT));
      }
      if (path.endsWith('/api/v1/projects/proj-1/settings/resolved')) {
        return Promise.resolve(
          jsonResponse({
            project: { usageUnmeasuredPolicy: 'deny' },
            settings: {},
          })
        );
      }
      if (path.endsWith('/api/v1/projects/proj-1/settings') && method === 'PUT') {
        putBodies.push(
          JSON.parse(typeof init?.body === 'string' ? init.body : '{}') as Record<string, unknown>
        );
        return Promise.resolve(jsonResponse({ usageUnmeasuredPolicy: 'deny' }));
      }
      if (path.endsWith('/api/v1/projects/proj-1/usage/budgets')) {
        return Promise.resolve(
          jsonResponse({
            scope: { kind: 'project', id: 'proj-1' },
            as_of: '2026-11-28T12:00:00Z',
            budgets: [
              {
                id: 'pb-1',
                limit_definition_id: 'def-1',
                limit_name: 'llm_tokens_per_project_day',
                scope_kind: 'project',
                scope_id: 'proj-1',
                window_start_utc: '2026-11-28T00:00:00Z',
                window_end_utc: '2026-11-29T00:00:00Z',
                cap: 2_000_000,
                token_action: 'pause_new',
                consumed: 1_800_000,
                state: 'warn',
                thresholds_notified: 1,
                override_until: null,
                state_version: 2,
                as_of: '2026-11-28T12:00:00Z',
              },
            ],
          })
        );
      }
      if (path.includes('/providers')) {
        return Promise.resolve(jsonResponse({ providers: [] }));
      }
      if (path.includes('/templates')) {
        return Promise.resolve(jsonResponse({ templates: [] }));
      }
      if (path.includes('/harness-configs')) {
        return Promise.resolve(jsonResponse({ harnessConfigs: [] }));
      }
      if (path.includes('/gcp-service-accounts')) {
        return Promise.resolve(jsonResponse({ serviceAccounts: [] }));
      }
      return Promise.resolve(jsonResponse({}, 404));
    });
  }

  it('hides unmeasured policy and token budgets panel when experiment is off', async () => {
    window.history.pushState({}, '', '/projects/proj-1/settings');
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, false);

    const putBodies: Array<Record<string, unknown>> = [];
    const fetchMock = stubProjectSettingsFetch(putBodies);
    vi.stubGlobal('fetch', fetchMock);

    element = document.createElement('scion-page-project-settings') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(element?.shadowRoot?.querySelector('sl-tab-panel[name="limits"]')).not.toBeNull();
    });

    expect(
      element.shadowRoot?.querySelector('[data-testid="project-unmeasured-policy-field"]')
    ).toBeNull();
    expect(
      element.shadowRoot?.querySelector('[data-testid="project-settings-token-budgets-section"]')
    ).toBeNull();
    expect(
      fetchMock.mock.calls.some(([u]) => {
        const s = typeof u === 'string' ? u : u instanceof URL ? u.href : u.url;
        return s.includes('/usage/budgets');
      })
    ).toBe(false);
  });

  it('renders unmeasured policy and project token budgets panel when enabled', async () => {
    window.history.pushState({}, '', '/projects/proj-1/settings');
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);

    const putBodies: Array<Record<string, unknown>> = [];
    vi.stubGlobal('fetch', stubProjectSettingsFetch(putBodies));

    element = document.createElement('scion-page-project-settings') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      const sel = element?.shadowRoot?.querySelector(
        '[data-testid="project-unmeasured-policy-select"]'
      );
      expect(sel).not.toBeNull();
      expect(sel?.getAttribute('value')).toBe('deny');
    });

    await vi.waitFor(() => {
      const panel = element?.shadowRoot?.querySelector('scion-token-budgets-panel');
      expect(panel).not.toBeNull();
      const badge = panel?.shadowRoot?.querySelector('[data-testid="budget-state-badge"]');
      expect(badge?.getAttribute('data-state')).toBe('warn');
    });
  });
});

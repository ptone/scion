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

function stubEventSource(): void {
  class MockEventSource {
    static readonly OPEN = 1;
    readyState = 1;
    addEventListener(): void {}
    removeEventListener(): void {}
    close(): void {}
  }
  vi.stubGlobal('EventSource', MockEventSource);
}

describe('scion-page-project-detail — budget status badges', () => {
  let element: (HTMLElement & { updateComplete: Promise<boolean> }) | null = null;

  beforeAll(async () => {
    await import('./project-detail.js');
  }, 60_000);

  afterEach(() => {
    element?.remove();
    element = null;
    delete window.__SCION_FEATURES__;
    resetServerFlagStateForTests();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  function stubProjectDetailFetch() {
    return vi.fn((url: string | URL | Request): Promise<Response> => {
      const path = typeof url === 'string' ? url : url instanceof URL ? url.href : url.url;
      if (path.endsWith('/api/v1/projects/proj-1')) {
        return Promise.resolve(
          jsonResponse({
            id: 'proj-1',
            name: 'Alpha',
            slug: 'alpha',
            agentCount: 1,
            created: '2026-11-01T00:00:00Z',
            updated: '2026-11-28T00:00:00Z',
          })
        );
      }
      if (path.endsWith('/api/v1/projects/proj-1/usage/budgets')) {
        return Promise.resolve(
          jsonResponse({
            scope: { kind: 'project', id: 'proj-1' },
            as_of: '2026-11-28T12:00:00Z',
            budgets: [
              {
                id: 'pb-day',
                limit_definition_id: 'def-day',
                limit_name: 'llm_tokens_per_project_day',
                scope_kind: 'project',
                scope_id: 'proj-1',
                window_start_utc: '2026-11-28T00:00:00Z',
                window_end_utc: '2026-11-29T00:00:00Z',
                cap: 1_000_000,
                token_action: 'pause_new',
                consumed: 1_100_000,
                state: 'exhausted',
                thresholds_notified: 3,
                override_until: null,
                state_version: 4,
                as_of: '2026-11-28T12:00:00Z',
              },
            ],
          })
        );
      }
      if (path.includes('/agents')) {
        return Promise.resolve(jsonResponse({ agents: [], totalCount: 0 }));
      }
      return Promise.resolve(jsonResponse({}, 404));
    });
  }

  it('hides budget badges and skips fetch when tokenomics.token_budgets is off', async () => {
    stubEventSource();
    window.history.pushState({}, '', '/projects/proj-1');
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, false);

    const fetchMock = stubProjectDetailFetch();
    vi.stubGlobal('fetch', fetchMock);

    element = document.createElement('scion-page-project-detail') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      expect(element?.shadowRoot?.textContent).toContain('Alpha');
    });

    expect(element.shadowRoot?.querySelector('[data-testid="project-budgets-summary"]')).toBeNull();
    expect(
      fetchMock.mock.calls.some(([u]) => {
        const s = typeof u === 'string' ? u : u instanceof URL ? u.href : u.url;
        return s.includes('/usage/budgets');
      })
    ).toBe(false);
  });

  it('renders project budget badges when enabled', async () => {
    stubEventSource();
    window.history.pushState({}, '', '/projects/proj-1');
    setFeatureFlag(TOKENOMICS_USAGE_LEDGER_FLAG, true);
    setFeatureFlag(TOKENOMICS_TOKEN_BUDGETS_FLAG, true);

    vi.stubGlobal('fetch', stubProjectDetailFetch());

    element = document.createElement('scion-page-project-detail') as HTMLElement & {
      updateComplete: Promise<boolean>;
    };
    document.body.appendChild(element);

    await vi.waitFor(() => {
      const badge = element?.shadowRoot?.querySelector(
        '[data-testid="project-budget-state-badge"]'
      );
      expect(badge).not.toBeNull();
      expect(badge?.getAttribute('data-state')).toBe('exhausted');
      expect(badge?.getAttribute('variant')).toBe('danger');
      const link = element?.shadowRoot?.querySelector(
        '[data-testid="project-manage-budgets-link"]'
      );
      expect(link?.getAttribute('href')).toBe('/projects/proj-1/settings?configTab=limits');
    });
  });
});
